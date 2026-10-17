package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/277pawan/freerev-cli/internal/redis"
)

// redisOptions mirrors a redis:// or rediss:// URL.
type redisOptions struct {
	Addr     string
	Username string
	Password string
	DB       int
	TLS      bool
}

// parseRedisURL converts a connection string into dial options.
//
// The password is held in memory only and is never printed: every message in
// this file uses parseRedisURL's redaction, not the raw URL.
func parseRedisURL(raw string) (redisOptions, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return redisOptions{}, fmt.Errorf("invalid redis url: %w", err)
	}
	if u.Scheme != "redis" && u.Scheme != "rediss" {
		return redisOptions{}, fmt.Errorf("unsupported scheme %q — use redis:// or rediss://", u.Scheme)
	}
	if u.Hostname() == "" {
		return redisOptions{}, errors.New("redis url is missing a host")
	}

	opts := redisOptions{
		Addr:     u.Host,
		Username: u.User.Username(),
		TLS:      u.Scheme == "rediss",
	}
	if opts.Addr == "" || !strings.Contains(opts.Addr, ":") {
		opts.Addr += ":6379"
	}
	if pw, ok := u.User.Password(); ok {
		opts.Password = pw
	}

	dbPart := strings.TrimPrefix(u.Path, "/")
	if dbPart != "" {
		parsed := 0
		if _, err := fmt.Sscanf(dbPart, "%d", &parsed); err == nil && parsed >= 0 {
			opts.DB = parsed
		}
	}
	return opts, nil
}

// redactRedisURL is the only way a URL reaches stdout or a log line.
func redactRedisURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "redis://<invalid>"
	}
	auth := ""
	if u.User != nil && u.User.Username() != "" {
		auth = u.User.Username() + ":***@"
	} else if u.User != nil {
		auth = "***@"
	}
	return fmt.Sprintf("%s://%s%s%s", u.Scheme, auth, u.Host, u.Path)
}

var redisProveJSON bool

// redisProveCmd proves that a Redis backup actually restores.
//
// This is the free path: no cloud account, no agent, no Docker. It runs on the
// customer's own machine, takes a BGSAVE, copies the RDB to a scratch dir,
// boots a throwaway redis-server against that copy, and compares the result
// with the live instance.
var redisProveCmd = &cobra.Command{
	Use:          "redis-prove",
	Short:        "Prove a self-hosted Redis backup actually restores",
	SilenceUsage: true,
	Long: `Connects to a Redis, forces a BGSAVE, copies the RDB to a scratch directory,
boots a throwaway redis-server against that copy, and reports whether the
backup loads and what came back.

Nothing is uploaded and no bytes leave the machine. The scratch directory and
the sandbox process are removed before the command exits.

  revenant-free redis-prove --url redis://localhost:6379
  revenant-free redis-prove --url rediss://:pw@cache.example.com:6380/0 --json`,
	RunE: func(cmd *cobra.Command, args []string) error {
		rawURL, _ := cmd.Flags().GetString("url")
		if strings.TrimSpace(rawURL) == "" {
			return errors.New("--url is required (redis://[:password]@host:port/db)")
		}
		opts, err := parseRedisURL(rawURL)
		if err != nil {
			return err
		}
		redacted := redactRedisURL(rawURL)

		serverBin, _ := cmd.Flags().GetString("redis-server-bin")
		workDir, _ := cmd.Flags().GetString("work-dir")
		maxMemory, _ := cmd.Flags().GetString("max-memory")
		sampleSize, _ := cmd.Flags().GetInt("sample-size")
		timeout, _ := cmd.Flags().GetDuration("timeout")

		// Progress goes to stderr in --json mode so stdout stays a single JSON
		// document. `--json | jq .restored` has to work.
		progress := io.Writer(os.Stdout)
		if redisProveJSON {
			progress = os.Stderr
		}

		fmt.Fprintf(progress, "Connecting to %s …\n", redacted)
		client, err := redis.Dial(redis.Options{
			Addr:     opts.Addr,
			Username: opts.Username,
			Password: opts.Password,
			DB:       opts.DB,
			TLS:      opts.TLS,
			Timeout:  10 * time.Second,
		})
		if err != nil {
			if errors.Is(err, context_DeadlineExceeded) || isNetworkError(err) {
				return fmt.Errorf(
					"could not reach %s — if Redis is on a private network, run this from inside it",
					redacted)
			}
			return fmt.Errorf("connect failed: %w", err)
		}
		defer client.Close()

		if _, err := client.Do("PING"); err != nil {
			return fmt.Errorf("PING failed: %w", err)
		}
		fmt.Fprintln(progress, "  PING           ok")

		server, _ := client.ServerInfo()
		fmt.Fprintf(progress, "  redis_version  %s\n", server["redis_version"])

		// Where is the RDB? CONFIG GET dir + dbfilename.
		cfgRaw, err := client.Do("CONFIG", "GET", "dir", "dbfilename")
		if err != nil {
			return fmt.Errorf("CONFIG GET dir/dbfilename failed: %w", err)
		}
		dir, filename := parseConfigPair(cfgRaw)
		if dir == "" || filename == "" {
			return errors.New("could not determine the RDB path — is CONFIG readable on this Redis?")
		}
		rdbPath := filepath.Join(dir, filename)
		fmt.Fprintf(progress, "  rdb            %s\n", rdbPath)

		// Force a fresh save. Returning before it finishes is how you end up
		// validating a stale dump.
		fmt.Fprintln(progress, "\nForcing BGSAVE …")
		if _, err := client.TriggerSave(timeout); err != nil {
			return fmt.Errorf("BGSAVE failed: %w", err)
		}
		if _, err := os.Stat(rdbPath); err != nil {
			return fmt.Errorf(
				"BGSAVE reported success but %s is not readable from this machine (%v). "+
					"If Redis runs in a container or on another host, run this inside that environment.",
				rdbPath, err)
		}

		before, _ := client.DBSize()
		digest, sampled, _ := client.SampleKeyspaceDigest(sampleSize)
		ttlBuckets, _ := client.TTLDistribution(sampleSize * 8)
		fmt.Fprintf(progress, "  source keys    %d (sampled %d for digest)\n", before, sampled)

		fmt.Fprintln(progress, "\nRestoring into a throwaway redis-server …")
		proof, err := redis.ProveRestore(client, rdbPath, redis.RestoreOptions{
			ServerBin:  serverBin,
			WorkDir:    workDir,
			MaxMemory:  maxMemory,
			Timeout:    timeout,
			SampleSize: sampleSize,
		})
		if err != nil {
			return fmt.Errorf("restore proof failed: %w", err)
		}

		// The sandbox digest was computed inside ProveRestore while the
		// throwaway server was still alive.
		restoredDigest, restoredSampled := proof.RestoredDigest, proof.RestoredSampled

		result := map[string]any{
			"redacted_url":           redacted,
			"redis_version":          proof.SourceVersion,
			"restored_version":       proof.RestoredVersion,
			"restored":               proof.Restored,
			"source_key_count":       proof.SourceKeyCount,
			"restored_key_count":     proof.RestoredKeyCount,
			"sampled_keys":           sampled,
			"restored_sampled_keys":  restoredSampled,
			"sample_digest":          digest,
			"restored_sample_digest": restoredDigest,
			"digest_match":           digest != "" && digest == restoredDigest,
			"rdb_bytes":              proof.RDBBytes,
			"restore_seconds":        proof.RestoreSeconds,
			"rpo_observed_seconds":   proof.RPOObservedSeconds,
			"rdb_last_bgsave_status": proof.RDBLastBgsaveStatus,
			"ttl_distribution":       ttlBuckets,
			"messages":               proof.Messages,
		}

		if redisProveJSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(result)
		}

		fmt.Println("\n── Restore proof ─────────────────────────────────────────")
		status := "PASS"
		if !proof.Restored {
			status = "FAIL"
		}
		fmt.Printf("  restored                %s\n", status)
		fmt.Printf("  source keys             %d\n", proof.SourceKeyCount)
		fmt.Printf("  restored keys           %d\n", proof.RestoredKeyCount)
		fmt.Printf("  rdb size                %d bytes\n", proof.RDBBytes)
		fmt.Printf("  restore time            %ds\n", proof.RestoreSeconds)
		fmt.Printf("  rpo observed            %ds\n", proof.RPOObservedSeconds)
		fmt.Printf("  last bgsave status      %s\n", proof.RDBLastBgsaveStatus)
		fmt.Printf("  digest match            %v\n", result["digest_match"])
		fmt.Println("  ttl distribution        " + fmtTtl(ttlBuckets))
		for _, m := range proof.Messages {
			fmt.Printf("  ! %s\n", m)
		}
		fmt.Println("──────────────────────────────────────────────────────────")
		fmt.Println("\nNo data was uploaded. The scratch copy and sandbox were removed.")

		if !proof.Restored {
			return errors.New("restore proof failed")
		}
		return nil
	},
}

// parseConfigPair extracts the dir and dbfilename values from a
// `CONFIG GET dir dbfilename` reply.
//
// Redis does not preserve the requested order — it answers with the parameters
// sorted by name, so asking for `dir dbfilename` comes back as
// `dbfilename, dump.rdb, dir, /var/lib/redis`. Reading this reply by position
// silently swaps the two and produces a nonsense path, so match on the name.
func parseConfigPair(v redis.Value) (string, string) {
	arr, ok := v.([]redis.Value)
	if !ok || len(arr) < 2 {
		return "", ""
	}

	var dir, filename string
	for i := 0; i+1 < len(arr); i += 2 {
		switch strings.ToLower(redis.Text(arr[i])) {
		case "dir":
			dir = redis.Text(arr[i+1])
		case "dbfilename":
			filename = redis.Text(arr[i+1])
		}
	}
	return dir, filename
}

func fmtTtl(buckets map[string]int64) string {
	order := []string{"no_expiry", "lt_1m", "lt_1h", "lt_1d", "gt_1d"}
	parts := make([]string, 0, len(order))
	for _, k := range order {
		parts = append(parts, fmt.Sprintf("%s=%d", k, buckets[k]))
	}
	return strings.Join(parts, " ")
}

// Minimal error classification without pulling in errors-as-a-service.
func isNetworkError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, s := range []string{
		"connection refused", "no such host", "i/o timeout",
		"cannot assign", "network is unreachable", "no route to host",
	} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// context_DeadlineExceeded is referenced only so the error string check above
// stays readable; net.Error timeouts are handled by isNetworkError.
var context_DeadlineExceeded = errors.New("context deadline exceeded")

func init() {
	redisProveCmd.Flags().String("url", "", "redis://[:password]@host:port/db")
	redisProveCmd.Flags().String("redis-server-bin", "", "path to redis-server (default: PATH)")
	redisProveCmd.Flags().String("work-dir", "", "scratch dir for the restored RDB (default: temp)")
	redisProveCmd.Flags().String("max-memory", "512mb", "maxmemory for the sandbox server")
	redisProveCmd.Flags().Int("sample-size", 25, "keys to sample for the digest")
	redisProveCmd.Flags().Duration("timeout", 2*time.Minute, "BGSAVE + restore timeout")
	redisProveCmd.Flags().BoolVar(&redisProveJSON, "json", false, "emit JSON instead of a table")
	rootCmd.AddCommand(redisProveCmd)
}
