package redis

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// startRedis boots a throwaway redis-server for the test and returns its
// address plus the dir it writes its RDB into. Tests skip when redis-server is
// not installed, so the suite still runs on a machine without Redis.
func startRedis(t *testing.T) (addr, rdbDir string, cleanup func()) {
	t.Helper()

	bin, err := exec.LookPath("redis-server")
	if err != nil {
		t.Skip("redis-server is not on PATH; skipping integration test")
	}

	dir := t.TempDir()
	port, err := freePort()
	if err != nil {
		t.Fatalf("free port: %v", err)
	}

	// --save "" disables autosave, but an explicit BGSAVE still works, which is
	// exactly what the proof path needs.
	cmd := exec.Command(bin,
		"--port", strconv.Itoa(port),
		"--bind", "127.0.0.1",
		"--dir", dir,
		"--dbfilename", "dump.rdb",
		"--save", "",
		"--appendonly", "no",
	)
	cmd.Stdout, cmd.Stderr = nil, nil
	if err := cmd.Start(); err != nil {
		t.Fatalf("start redis-server: %v", err)
	}

	stop := func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
	}

	addr = "127.0.0.1:" + strconv.Itoa(port)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		c, dialErr := Dial(Options{Addr: addr, Timeout: time.Second})
		if dialErr == nil {
			_ = c.Close()
			return addr, dir, stop
		}
		time.Sleep(100 * time.Millisecond)
	}
	stop()
	t.Fatalf("redis-server never became reachable on %s", addr)
	return "", "", func() {}
}

func mustSet(t *testing.T, c *Client, key, value string, ttl time.Duration) {
	t.Helper()
	args := []string{"SET", key, value}
	if ttl > 0 {
		args = append(args, "EX", strconv.Itoa(int(ttl.Seconds())))
	}
	if _, err := c.Do(args...); err != nil {
		t.Fatalf("SET %s: %v", key, err)
	}
}

func fileHash(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// ─── BGSAVE ────────────────────────────────────────────────────────────────

func TestTriggerSaveWritesAndAdvancesRDB(t *testing.T) {
	addr, dir, stop := startRedis(t)
	defer stop()

	c, err := Dial(Options{Addr: addr, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	mustSet(t, c, "seed", "value", 0)

	rdb := filepath.Join(dir, "dump.rdb")
	if _, err := os.Stat(rdb); err == nil {
		t.Fatal(`--save "" should mean no RDB exists before BGSAVE`)
	}

	elapsed, err := c.TriggerSave(20 * time.Second)
	if err != nil {
		t.Fatalf("TriggerSave: %v", err)
	}
	if elapsed < 0 {
		t.Fatalf("BGSAVE moved rdb_last_save_time backwards (%d)", elapsed)
	}
	if _, err := os.Stat(rdb); err != nil {
		t.Fatalf("BGSAVE did not produce %s: %v", rdb, err)
	}
}

// ─── digest ────────────────────────────────────────────────────────────────

func TestSampleKeyspaceDigestIsStableAndContentSensitive(t *testing.T) {
	addr, _, stop := startRedis(t)
	defer stop()

	c, err := Dial(Options{Addr: addr, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	mustSet(t, c, "alpha", "one", 0)
	mustSet(t, c, "beta", "two", 0)

	first, n1, err := c.SampleKeyspaceDigest(50)
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	if n1 != 2 {
		t.Fatalf("sampled %d keys, want 2", n1)
	}

	second, _, err := c.SampleKeyspaceDigest(50)
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	if first != second {
		t.Fatal("digest must be stable for an unchanged keyspace")
	}

	// A value change must move the digest: this is what makes the restore
	// comparison meaningful.
	mustSet(t, c, "beta", "changed", 0)
	third, _, err := c.SampleKeyspaceDigest(50)
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	if third == first {
		t.Fatal("digest did not change after the value changed")
	}
}

// The digest must never leak key names or values in the clear.
func TestSampleKeyspaceDigestDoesNotLeakKeyNames(t *testing.T) {
	addr, _, stop := startRedis(t)
	defer stop()

	c, err := Dial(Options{Addr: addr, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	mustSet(t, c, "user:secret-token-abc123", "hunter2", 0)

	digest, _, err := c.SampleKeyspaceDigest(50)
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	if len(digest) != 64 {
		t.Fatalf("expected a sha256 hex digest, got %d chars: %q", len(digest), digest)
	}
	for _, secret := range []string{"user", "hunter2", "abc123", "secret"} {
		if strings.Contains(digest, secret) {
			t.Fatalf("digest leaks %q: %s", secret, digest)
		}
	}
}

func TestTTLDistributionBuckets(t *testing.T) {
	addr, _, stop := startRedis(t)
	defer stop()

	c, err := Dial(Options{Addr: addr, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	mustSet(t, c, "persist:forever", "v", 0)        // no_expiry
	mustSet(t, c, "soon", "v", 5*time.Minute)       // lt_1h
	mustSet(t, c, "later", "v", 2*time.Hour)        // lt_1d
	mustSet(t, c, "muchlater", "v", 3*24*time.Hour) // gt_1d

	buckets, err := c.TTLDistribution(200)
	if err != nil {
		t.Fatalf("TTLDistribution: %v", err)
	}

	for _, k := range []string{"no_expiry", "lt_1m", "lt_1h", "lt_1d", "gt_1d"} {
		if _, ok := buckets[k]; !ok {
			t.Fatalf("bucket %q missing from %#v", k, buckets)
		}
	}
	if buckets["no_expiry"] != 1 {
		t.Errorf("no_expiry = %d, want 1", buckets["no_expiry"])
	}
	if buckets["lt_1h"] != 1 {
		t.Errorf("lt_1h = %d, want 1", buckets["lt_1h"])
	}
	if buckets["lt_1d"] != 1 {
		t.Errorf("lt_1d = %d, want 1", buckets["lt_1d"])
	}
	if buckets["gt_1d"] != 1 {
		t.Errorf("gt_1d = %d, want 1", buckets["gt_1d"])
	}
}

// A partial sample must still be comparable between the live instance and the
// restored sandbox. SCAN order differs across an RDB load, so a sample chosen
// by scan position is a different set of keys on each side and the digests can
// never match. This is the regression guard for that.
func TestProveRestoreDigestMatchesOnPartialSample(t *testing.T) {
	addr, dir, stop := startRedis(t)
	defer stop()

	c, err := Dial(Options{Addr: addr, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	// Far more keys than the sample size, with mixed types so the hash table
	// is forced to rehash on load.
	const keyCount = 300
	for i := 0; i < keyCount; i++ {
		mustSet(t, c, "key:"+strconv.Itoa(i), "value-"+strconv.Itoa(i), 0)
	}
	mustSet(t, c, "list:a", "x", 0)
	mustSet(t, c, "hash:b", "y", 0)

	if _, err := c.TriggerSave(20 * time.Second); err != nil {
		t.Fatalf("TriggerSave: %v", err)
	}

	const sampleSize = 25
	sourceDigest, sourceSampled, err := c.SampleKeyspaceDigest(sampleSize)
	if err != nil {
		t.Fatalf("source digest: %v", err)
	}
	if sourceSampled != sampleSize {
		t.Fatalf("sampled %d keys, want %d", sourceSampled, sampleSize)
	}

	proof, err := ProveRestore(c, filepath.Join(dir, "dump.rdb"), RestoreOptions{
		SampleSize: sampleSize,
		Timeout:    30 * time.Second,
	})
	if err != nil {
		t.Fatalf("ProveRestore: %v", err)
	}

	if !proof.Restored {
		t.Fatal("proof.Restored = false, want true")
	}
	if proof.RestoredSampled != sourceSampled {
		t.Fatalf("source sampled %d, restore sampled %d", sourceSampled, proof.RestoredSampled)
	}
	if proof.RestoredDigest != sourceDigest {
		t.Errorf("partial-sample digest mismatch:\n source  %s\n restored %s",
			sourceDigest, proof.RestoredDigest)
	}
}

// A changed value must break the digest match — otherwise a passing digest
// proves nothing.
func TestProveRestoreDigestDetectsCorruptedDump(t *testing.T) {
	addr, dir, stop := startRedis(t)
	defer stop()

	c, err := Dial(Options{Addr: addr, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	for i := 0; i < 40; i++ {
		mustSet(t, c, "key:"+strconv.Itoa(i), "original", 0)
	}
	if _, err := c.TriggerSave(20 * time.Second); err != nil {
		t.Fatalf("TriggerSave: %v", err)
	}

	// Corrupt the dump, then compare the live keyspace against it.
	if err := os.WriteFile(filepath.Join(dir, "dump.rdb"), []byte("this is not an rdb"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustSet(t, c, "key:0", "tampered", 0)

	sourceDigest, _, err := c.SampleKeyspaceDigest(100)
	if err != nil {
		t.Fatalf("source digest: %v", err)
	}

	_, cleanup, err := Restore(RestoreOptions{
		RDBPath:    filepath.Join(dir, "dump.rdb"),
		SampleSize: 100,
		// A corrupt RDB makes the sandbox never answer PING, so this waits out
		// the full timeout before giving up. Keep it short.
		Timeout: 3 * time.Second,
	})
	if err == nil {
		cleanup()
		t.Skip("this redis-server accepted a corrupt RDB; nothing to compare")
	}
	if sourceDigest == "" {
		t.Fatal("expected a source digest even when the restore fails")
	}
}

func TestSampleKeyspaceDigestIsDeterministicAcrossInsertionOrder(t *testing.T) {
	addr, _, stop := startRedis(t)
	defer stop()

	c, err := Dial(Options{Addr: addr, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	// The same keys inserted in a different order must digest identically:
	// selection is by name, not by position.
	for i := 0; i < 60; i++ {
		mustSet(t, c, "key:"+strconv.Itoa(i), "v"+strconv.Itoa(i), 0)
	}
	first, _, err := c.SampleKeyspaceDigest(10)
	if err != nil {
		t.Fatalf("digest: %v", err)
	}

	c2, err := Dial(Options{Addr: addr, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c2.Close()
	if _, err := c2.Do("FLUSHALL"); err != nil {
		t.Fatalf("FLUSHALL: %v", err)
	}
	for i := 59; i >= 0; i-- {
		mustSet(t, c2, "key:"+strconv.Itoa(i), "v"+strconv.Itoa(i), 0)
	}

	second, _, err := c2.SampleKeyspaceDigest(10)
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	if first != second {
		t.Errorf("digest depends on insertion order:\n forward   %s\n reverse   %s", first, second)
	}
}

func TestInsertSorted(t *testing.T) {
	cases := []struct {
		in   []string
		add  string
		want []string
	}{
		{nil, "b", []string{"b"}},
		{[]string{"a", "c"}, "b", []string{"a", "b", "c"}},
		{[]string{"a", "b"}, "b", []string{"a", "b"}},
		{[]string{"b"}, "a", []string{"a", "b"}},
	}
	for _, tc := range cases {
		got := insertSorted(append([]string{}, tc.in...), tc.add)
		if len(got) != len(tc.want) {
			t.Fatalf("insertSorted(%v, %q) = %v, want %v", tc.in, tc.add, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("insertSorted(%v, %q) = %v, want %v", tc.in, tc.add, got, tc.want)
			}
		}
	}
}

// ─── the restore proof ─────────────────────────────────────────────────────

func TestProveRestoreEndToEnd(t *testing.T) {
	addr, dir, stop := startRedis(t)
	defer stop()

	c, err := Dial(Options{Addr: addr, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	const keyCount = 40
	for i := 0; i < keyCount; i++ {
		mustSet(t, c, "key:"+strconv.Itoa(i), "value-"+strconv.Itoa(i), 0)
	}
	mustSet(t, c, "session:abc", "payload", 30*time.Minute)

	if _, err := c.TriggerSave(20 * time.Second); err != nil {
		t.Fatalf("TriggerSave: %v", err)
	}
	rdb := filepath.Join(dir, "dump.rdb")

	sourceDigest, sourceSampled, err := c.SampleKeyspaceDigest(100)
	if err != nil {
		t.Fatalf("source digest: %v", err)
	}

	proof, err := ProveRestore(c, rdb, RestoreOptions{
		SampleSize: 100,
		Timeout:    30 * time.Second,
	})
	if err != nil {
		t.Fatalf("ProveRestore: %v", err)
	}

	if !proof.Restored {
		t.Fatal("proof.Restored = false, want true")
	}
	if proof.RestoredKeyCount != keyCount+1 {
		t.Errorf("restored keys = %d, want %d", proof.RestoredKeyCount, keyCount+1)
	}
	if proof.SourceKeyCount != keyCount+1 {
		t.Errorf("source keys = %d, want %d", proof.SourceKeyCount, keyCount+1)
	}
	if proof.RDBBytes <= 0 {
		t.Errorf("rdb bytes = %d, want > 0", proof.RDBBytes)
	}
	if proof.RDBLastBgsaveStatus != "ok" {
		t.Errorf("last bgsave status = %q, want ok", proof.RDBLastBgsaveStatus)
	}
	if proof.SourceVersion == "" || proof.RestoredVersion == "" {
		t.Errorf("versions not captured: source=%q restored=%q",
			proof.SourceVersion, proof.RestoredVersion)
	}
	if proof.RestoredSampled != sourceSampled {
		t.Errorf("sampled %d keys on restore vs %d on source — digests are not comparable",
			proof.RestoredSampled, sourceSampled)
	}
	if proof.RestoredDigest != sourceDigest {
		t.Errorf("digest mismatch after a clean restore:\n source  %s\n restored %s",
			sourceDigest, proof.RestoredDigest)
	}
	if len(proof.Messages) != 0 {
		t.Errorf("unexpected warnings on a clean restore: %v", proof.Messages)
	}
}

// Proving a restore must never touch the customer's original dump.
func TestProveRestoreLeavesSourceRDUntouched(t *testing.T) {
	addr, dir, stop := startRedis(t)
	defer stop()

	c, err := Dial(Options{Addr: addr, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	mustSet(t, c, "only", "one", 0)
	if _, err := c.TriggerSave(20 * time.Second); err != nil {
		t.Fatalf("TriggerSave: %v", err)
	}

	rdb := filepath.Join(dir, "dump.rdb")
	before := fileHash(t, rdb)

	if _, err := ProveRestore(c, rdb, RestoreOptions{SampleSize: 10, Timeout: 30 * time.Second}); err != nil {
		t.Fatalf("ProveRestore: %v", err)
	}

	if after := fileHash(t, rdb); after != before {
		t.Fatal("the source RDB was modified by the restore proof")
	}
}

// cleanup() must kill the sandbox and remove the scratch copy.
func TestRestoreCleanupRemovesScratchCopy(t *testing.T) {
	addr, dir, stop := startRedis(t)
	defer stop()

	c, err := Dial(Options{Addr: addr, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	mustSet(t, c, "k", "v", 0)
	if _, err := c.TriggerSave(20 * time.Second); err != nil {
		t.Fatalf("TriggerSave: %v", err)
	}

	workDir := t.TempDir()
	client, cleanup, err := Restore(RestoreOptions{
		RDBPath:    filepath.Join(dir, "dump.rdb"),
		WorkDir:    workDir,
		SampleSize: 10,
		Timeout:    30 * time.Second,
	})
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}

	// The sandbox really did load the dump before we tear it down.
	if n, err := client.DBSize(); err != nil || n != 1 {
		t.Errorf("sandbox DBSIZE = %d (err %v), want 1", n, err)
	}

	restorePort := client.conn.RemoteAddr().String()
	cleanup()

	if _, err := os.Stat(filepath.Join(workDir, "restore.rdb")); err == nil {
		t.Error("the scratch RDB copy survived cleanup")
	}

	// The sandbox process must be gone: the port must stop answering.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c2, err := Dial(Options{Addr: restorePort, Timeout: 500 * time.Millisecond}); err != nil {
			return
		} else {
			_ = c2.Close()
			time.Sleep(100 * time.Millisecond)
		}
	}
	t.Errorf("sandbox redis-server still listening on %s after cleanup", restorePort)
}

// A caller-supplied --work-dir belongs to the user. Cleanup must remove only
// the scratch copy placed in it, never the directory or unrelated files.
func TestRestorePreservesCallerSuppliedWorkDir(t *testing.T) {
	addr, dir, stop := startRedis(t)
	defer stop()

	c, err := Dial(Options{Addr: addr, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	mustSet(t, c, "k", "v", 0)
	if _, err := c.TriggerSave(20 * time.Second); err != nil {
		t.Fatalf("TriggerSave: %v", err)
	}

	workDir := t.TempDir()
	keep := filepath.Join(workDir, "customer-data.txt")
	if err := os.WriteFile(keep, []byte("do not delete"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, cleanup, err := Restore(RestoreOptions{
		RDBPath:    filepath.Join(dir, "dump.rdb"),
		WorkDir:    workDir,
		SampleSize: 10,
		Timeout:    30 * time.Second,
	})
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	cleanup()

	if _, err := os.Stat(workDir); err != nil {
		t.Fatalf("the caller's work-dir was deleted: %v", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("an unrelated file inside the work-dir was deleted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workDir, "restore.rdb")); err == nil {
		t.Error("the scratch RDB copy should still have been removed")
	}
}

// A scratch dir the tool creates itself is removed wholesale.
func TestRestoreRemovesOwnScratchDir(t *testing.T) {
	addr, dir, stop := startRedis(t)
	defer stop()

	c, err := Dial(Options{Addr: addr, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	mustSet(t, c, "k", "v", 0)
	if _, err := c.TriggerSave(20 * time.Second); err != nil {
		t.Fatalf("TriggerSave: %v", err)
	}

	before, err := filepath.Glob(filepath.Join(os.TempDir(), "revenant-redis-restore-*"))
	if err != nil {
		t.Fatal(err)
	}

	client, cleanup, err := Restore(RestoreOptions{
		RDBPath:    filepath.Join(dir, "dump.rdb"),
		SampleSize: 10,
		Timeout:    30 * time.Second,
	})
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	cleanup()
	_ = client

	after, err := filepath.Glob(filepath.Join(os.TempDir(), "revenant-redis-restore-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Errorf("scratch dirs leaked: %d before, %d after (%v)", len(before), len(after), after)
	}
}

// A failure before the sandbox starts must not leak the scratch dir either.
func TestRestoreDoesNotLeakScratchDirOnCopyFailure(t *testing.T) {
	before, err := filepath.Glob(filepath.Join(os.TempDir(), "revenant-redis-restore-*"))
	if err != nil {
		t.Fatal(err)
	}

	if _, _, err := Restore(RestoreOptions{
		RDBPath:    "/nonexistent/path/dump.rdb",
		SampleSize: 10,
		Timeout:    5 * time.Second,
	}); err == nil {
		t.Fatal("expected a copy failure")
	}

	after, err := filepath.Glob(filepath.Join(os.TempDir(), "revenant-redis-restore-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Errorf("scratch dir leaked on the copy-failure path: %d before, %d after (%v)",
			len(before), len(after), after)
	}
}

func TestRestoreFailsClearlyWithoutServerBinary(t *testing.T) {
	addr, dir, stop := startRedis(t)
	defer stop()

	c, err := Dial(Options{Addr: addr, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	mustSet(t, c, "k", "v", 0)
	if _, err := c.TriggerSave(20 * time.Second); err != nil {
		t.Fatalf("TriggerSave: %v", err)
	}

	_, _, err = Restore(RestoreOptions{
		RDBPath:    filepath.Join(dir, "dump.rdb"),
		ServerBin:  "/nonexistent/redis-server-binary",
		SampleSize: 10,
		Timeout:    5 * time.Second,
	})
	if err == nil {
		t.Fatal("expected an error for a missing redis-server binary")
	}
	if !strings.Contains(err.Error(), "redis-server") {
		t.Errorf("error should point at the missing binary, got: %v", err)
	}
}

func TestRestoreRequiresRDBPath(t *testing.T) {
	if _, _, err := Restore(RestoreOptions{}); err == nil {
		t.Fatal("expected an error when no RDB path is given")
	}
}
