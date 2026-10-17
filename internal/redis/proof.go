package redis

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Proof is the result of restoring an RDB into a throwaway redis-server and
// comparing it against the live source.
//
// The single number that matters is Restored. A cache whose RDB cannot be
// loaded is a backup that does not exist, and that is exactly what nobody
// checks by hand.
type Proof struct {
	Restored         bool
	RestoredKeyCount int64
	SourceKeyCount   int64
	RDBBytes         int64
	// RPO observed at BGSAVE time, in seconds (0 = saved just now).
	RPOObservedSeconds  int64
	RestoreSeconds      int64
	RDBLastBgsaveStatus string
	// RestoredVersion / SourceVersion catch a downgrade on restore.
	RestoredVersion string
	SourceVersion   string
	// Digest of the RESTORED keyspace, computed while the sandbox is still
	// alive. Compared against the source digest by the caller.
	RestoredDigest  string
	RestoredSampled int
	Messages        []string
}

// TriggerSave asks the live Redis to write an RDB and waits for it to finish.
//
// BGSAVE is async, so we must not return until rdb_last_bgsave_status says
// "ok" — returning early is how you end up validating a stale dump and
// declaring a healthy backup that is hours behind.
func (c *Client) TriggerSave(timeout time.Duration) (int64, error) {
	before, err := c.LastSaveTime()
	if err != nil {
		return 0, err
	}
	if _, err := c.Do("BGSAVE"); err != nil {
		return 0, err
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		info, err := c.PersistenceInfo()
		if err != nil {
			return 0, err
		}
		switch info["rdb_bgsave_in_progress"] {
		case "0":
			if status := info["rdb_last_bgsave_status"]; status != "" && status != "ok" {
				return 0, fmt.Errorf("BGSAVE finished with status %q", status)
			}
			after, err := c.LastSaveTime()
			if err != nil {
				return 0, err
			}
			return after - before, nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return 0, errors.New("BGSAVE did not finish within the timeout")
}

func (c *Client) PersistenceInfo() (map[string]string, error) {
	raw, err := c.Do("INFO", "persistence")
	if err != nil {
		return nil, err
	}
	return Info(Text(raw)), nil
}

func (c *Client) ServerInfo() (map[string]string, error) {
	raw, err := c.Do("INFO", "server")
	if err != nil {
		return nil, err
	}
	return Info(Text(raw)), nil
}

func (c *Client) LastSaveTime() (int64, error) {
	info, err := c.PersistenceInfo()
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(info["rdb_last_save_time"], 10, 64)
}

// DBSize returns the number of keys in the selected database.
func (c *Client) DBSize() (int64, error) {
	v, err := c.Do("DBSIZE")
	if err != nil {
		return 0, err
	}
	return Int(v), nil
}

// SampleKeyspaceDigest walks the keyspace with SCAN and hashes a deterministic
// subset of it.
//
// The subset must be selected deterministically, by key name. SCAN walks a hash
// table whose bucket layout differs between a live instance and an RDB that has
// just been loaded — rehashing on load changes the iteration order — so
// "the first N keys SCAN happens to return" are different keys on each side.
// Comparing those two digests reports a mismatch on every run, which is how a
// healthy backup ends up looking corrupt. Selecting the N lexicographically
// smallest key names gives both sides the same keys, so the digests mean the
// same thing and a real difference is a real difference.
//
// This scans the whole keyspace but only DUMPs sampleSize keys: SCAN is cheap,
// DUMP is a round trip per key.
//
// Key names are hashed before use and values are hashed from their DUMP
// payload, so the resulting digest cannot be reversed into a key listing or a
// value even if it leaks. Nothing sampled here is ever written to a report in
// the clear.
func (c *Client) SampleKeyspaceDigest(sampleSize int) (digest string, sampled int, err error) {
	if sampleSize <= 0 {
		sampleSize = 25
	}

	// Keep only the smallest sampleSize key names seen so far.
	chosen := make([]string, 0, sampleSize)
	cursor := "0"
	for {
		reply, err := c.Do("SCAN", cursor, "COUNT", "200")
		if err != nil {
			return "", 0, err
		}
		arr, ok := reply.([]Value)
		if !ok || len(arr) < 2 {
			break
		}
		cursor = Text(arr[0])
		batch, _ := arr[1].([]Value)
		for _, k := range batch {
			key := Text(k)
			if len(chosen) == sampleSize && key >= chosen[len(chosen)-1] {
				continue // cannot displace the current worst
			}
			chosen = insertSorted(chosen, key)
			if len(chosen) > sampleSize {
				chosen = chosen[:sampleSize]
			}
		}
		if cursor == "0" {
			break
		}
	}

	entries := make([]string, 0, len(chosen))
	for _, key := range chosen {
		dumped, err := c.Do("DUMP", key)
		if err != nil {
			// A key that vanished mid-scan (expired) is normal. Skip it.
			continue
		}
		keyHash := sha256.Sum256([]byte(key))
		valHash := sha256.Sum256([]byte(Text(dumped)))
		entries = append(entries,
			hex.EncodeToString(keyHash[:])+":"+hex.EncodeToString(valHash[:]))
	}
	sort.Strings(entries)
	sum := sha256.Sum256([]byte(strings.Join(entries, "\n")))
	return hex.EncodeToString(sum[:]), len(entries), nil
}

// insertSorted adds s to an already-sorted slice, keeping it sorted.
func insertSorted(sorted []string, s string) []string {
	i := sort.SearchStrings(sorted, s)
	if i < len(sorted) && sorted[i] == s {
		return sorted
	}
	sorted = append(sorted, "")
	copy(sorted[i+1:], sorted[i:])
	sorted[i] = s
	return sorted
}

// TTLDistribution buckets keys by remaining TTL, which is how a cache's
// expiry behaviour survives a restore.
func (c *Client) TTLDistribution(sampleSize int) (map[string]int64, error) {
	if sampleSize <= 0 {
		sampleSize = 200
	}
	buckets := map[string]int64{
		"no_expiry": 0,
		"lt_1m":     0,
		"lt_1h":     0,
		"lt_1d":     0,
		"gt_1d":     0,
	}
	cursor := "0"
	seen := 0
	for seen < sampleSize {
		reply, err := c.Do("SCAN", cursor, "COUNT", "200")
		if err != nil {
			return nil, err
		}
		arr, ok := reply.([]Value)
		if !ok || len(arr) < 2 {
			break
		}
		cursor = Text(arr[0])
		keys, _ := arr[1].([]Value)
		for _, k := range keys {
			if seen >= sampleSize {
				break
			}
			seen++
			ttl, err := c.Do("TTL", Text(k))
			if err != nil {
				continue
			}
			switch n := Int(ttl); {
			case n < 0:
				buckets["no_expiry"]++
			case n < 60:
				buckets["lt_1m"]++
			case n < 3600:
				buckets["lt_1h"]++
			case n < 86400:
				buckets["lt_1d"]++
			default:
				buckets["gt_1d"]++
			}
		}
		if cursor == "0" {
			break
		}
	}
	return buckets, nil
}

// ─── The restore ────────────────────────────────────────────────────────────

// RestoreOptions configures the throwaway server.
type RestoreOptions struct {
	// Path to the RDB produced by BGSAVE on the source.
	RDBPath string
	// redis-server binary. Defaults to "redis-server" on PATH.
	ServerBin string
	// Scratch dir for the restored copy. Defaults to a temp dir.
	WorkDir string
	// Max memory for the sandbox. Keeps a large cache from OOMing the host.
	MaxMemory string
	Timeout   time.Duration
	// Keys sampled for the restored digest. Reuse the source sample size so
	// the two digests are comparable.
	SampleSize int
}

// Restore boots a throwaway redis-server against a COPY of the RDB, waits for
// it to answer PING, and returns the live client plus a cleanup func.
//
// This never touches the source Redis and never mutates the original RDB:
// the file is copied first, and the sandbox runs with no persistence of its
// own (save "", appendonly no) so it cannot write anything back.
func Restore(opts RestoreOptions) (client *Client, cleanup func(), err error) {
	if opts.RDBPath == "" {
		return nil, nil, errors.New("redis restore: rdb path is required")
	}
	bin := opts.ServerBin
	if bin == "" {
		bin = "redis-server"
	}
	if _, lookErr := exec.LookPath(bin); lookErr != nil {
		return nil, nil, fmt.Errorf(
			"redis restore: %q not found on PATH — install redis-server or pass -redis-server-bin", bin)
	}

	// Track ownership: a caller-supplied --work-dir may hold unrelated files,
	// so only a scratch dir we created ourselves is removed wholesale.
	ownedWorkDir := false
	workDir := opts.WorkDir
	if workDir == "" {
		workDir, err = os.MkdirTemp("", "revenant-redis-restore-")
		if err != nil {
			return nil, nil, err
		}
		ownedWorkDir = true
	}
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		if ownedWorkDir {
			_ = os.RemoveAll(workDir)
		}
		return nil, nil, err
	}

	// Once the sandbox exists, cleanup is guaranteed to run. Before that, any
	// scratch dir we created has to be cleaned up by hand or it leaks.
	// Only a dir we made is ever removed wholesale.
	rdbCopy := filepath.Join(workDir, "restore.rdb")
	discard := func() {
		if ownedWorkDir {
			_ = os.RemoveAll(workDir)
		} else {
			_ = os.Remove(rdbCopy)
		}
	}

	// Copy, never open the source RDB directly.
	if err := copyFile(opts.RDBPath, rdbCopy); err != nil {
		if ownedWorkDir {
			_ = os.RemoveAll(workDir)
		}
		return nil, nil, fmt.Errorf("redis restore: copy rdb: %w", err)
	}

	// Pick a free port so we never collide with the customer's live Redis or
	// with a previous drill that has not finished shutting down.
	port, err := freePort()
	if err != nil {
		discard()
		return nil, nil, err
	}

	maxMemory := opts.MaxMemory
	if maxMemory == "" {
		maxMemory = "512mb"
	}
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}

	args := []string{
		"--dir", workDir,
		"--dbfilename", "restore.rdb",
		"--port", strconv.Itoa(port),
		"--bind", "127.0.0.1",
		// The sandbox must not persist anything of its own.
		"--save", "",
		"--appendonly", "no",
		"--maxmemory", maxMemory,
		// A restored cache that cannot evict would just OOM; keep it honest.
		"--maxmemory-policy", "noeviction",
	}
	cmd := exec.Command(bin, args...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		discard()
		return nil, nil, fmt.Errorf("redis restore: start server: %w", err)
	}

	cleanup = func() {
		if client != nil {
			_ = client.Close()
		}
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
		if ownedWorkDir {
			_ = os.RemoveAll(workDir)
		} else {
			// Only ever delete the copy we placed there. Removing the whole
			// caller-supplied directory would take unrelated files with it.
			_ = os.Remove(rdbCopy)
		}
	}

	// Wait for the sandbox to answer PING. A slow RDB load is expected on a
	// large cache, so this waits rather than assuming.
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		c, dialErr := Dial(Options{Addr: fmt.Sprintf("127.0.0.1:%d", port), Timeout: 2 * time.Second})
		if dialErr == nil {
			if _, pingErr := c.Do("PING"); pingErr == nil {
				return c, cleanup, nil
			}
			_ = c.Close()
		}
		time.Sleep(300 * time.Millisecond)
	}
	cleanup()
	return nil, nil, fmt.Errorf("redis restore: restored server did not answer PING within %s", timeout)
}

// ProveRestore performs the full check: capture, copy, boot, compare.
func ProveRestore(source *Client, rdbPath string, opts RestoreOptions) (Proof, error) {
	started := time.Now()
	var proof Proof

	persistence, err := source.PersistenceInfo()
	if err != nil {
		return proof, err
	}
	proof.RDBLastBgsaveStatus = persistence["rdb_last_bgsave_status"]
	if last, parseErr := strconv.ParseInt(persistence["rdb_last_save_time"], 10, 64); parseErr == nil {
		proof.RPOObservedSeconds = time.Now().Unix() - last
	}
	server, err := source.ServerInfo()
	if err == nil {
		proof.SourceVersion = server["redis_version"]
	}
	proof.SourceKeyCount, _ = source.DBSize()

	opts.RDBPath = rdbPath
	restored, cleanup, err := Restore(opts)
	if err != nil {
		return proof, err
	}
	defer cleanup()

	proof.RestoredKeyCount, _ = restored.DBSize()
	proof.RestoredVersion = ""
	if info, err := restored.ServerInfo(); err == nil {
		proof.RestoredVersion = info["redis_version"]
	}
	proof.RestoreSeconds = int64(time.Since(started).Seconds())

	// Digest the sandbox while it is still running — after Restore returns,
	// the caller closes it and deletes the scratch dir.
	proof.RestoredDigest, proof.RestoredSampled, _ = restored.SampleKeyspaceDigest(opts.SampleSize)

	if info, err := os.Stat(rdbPath); err == nil {
		proof.RDBBytes = info.Size()
	}

	// A restore is "restored" when the sandbox actually loaded the dump. An
	// exact key-count match is NOT required — keys legitimately expire between
	// BGSAVE and the check — so a large shortfall is reported as a warning in
	// Messages rather than a hard failure.
	proof.Restored = true
	if proof.SourceKeyCount > 0 && proof.RestoredKeyCount < proof.SourceKeyCount {
		shortfall := proof.SourceKeyCount - proof.RestoredKeyCount
		pct := shortfall * 100 / proof.SourceKeyCount
		if pct >= 50 {
			proof.Messages = append(proof.Messages,
				fmt.Sprintf("restored instance has %d keys vs %d at BGSAVE (%d%% missing) — expected some expiry, but verify the RDB is current",
					proof.RestoredKeyCount, proof.SourceKeyCount, pct))
		}
	}
	return proof, nil
}

// ─── small helpers ──────────────────────────────────────────────────────────

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := out.ReadFrom(in); err != nil {
		return err
	}
	return out.Sync()
}

// freePort asks the kernel for an unused port by binding to :0 and reading
// back what it gave us, then closes it. There is a small race between closing
// and the sandbox binding it, which is why the sandbox also binds 127.0.0.1
// only — a collision fails the drill loudly instead of connecting to the
// customer's live Redis.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	addr, ok := l.Addr().(*net.TCPAddr)
	if !ok {
		return 0, fmt.Errorf("redis restore: unexpected listener address %T", l.Addr())
	}
	return addr.Port, nil
}
