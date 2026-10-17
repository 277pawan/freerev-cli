package cmd

import (
	"errors"
	"strings"
	"testing"

	"github.com/277pawan/freerev-cli/internal/redis"
)

func TestParseRedisURL(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		addr     string
		username string
		password string
		db       int
		tls      bool
	}{
		{
			name: "host only gets the default port",
			in:   "redis://localhost",
			addr: "localhost:6379",
		},
		{
			name: "explicit port",
			in:   "redis://cache.internal:6380",
			addr: "cache.internal:6380",
		},
		{
			name:     "password only",
			in:       "redis://:hunter2@cache:6379",
			addr:     "cache:6379",
			password: "hunter2",
		},
		{
			name:     "username and password",
			in:       "redis://app:s3cret@cache:6379",
			addr:     "cache:6379",
			username: "app",
			password: "s3cret",
		},
		{
			name: "database index",
			in:   "redis://cache:6379/3",
			addr: "cache:6379",
			db:   3,
		},
		{
			name: "rediss implies TLS",
			in:   "rediss://cache:6380/1",
			addr: "cache:6380",
			db:   1,
			tls:  true,
		},
		{
			name:     "surrounding whitespace is tolerated",
			in:       "  redis://cache:6379  ",
			addr:     "cache:6379",
			username: "",
		},
		{
			name: "a non-numeric path leaves the db at 0",
			in:   "redis://cache:6379/notanumber",
			addr: "cache:6379",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseRedisURL(tc.in)
			if err != nil {
				t.Fatalf("parseRedisURL(%q): %v", tc.in, err)
			}
			if got.Addr != tc.addr {
				t.Errorf("Addr = %q, want %q", got.Addr, tc.addr)
			}
			if got.Username != tc.username {
				t.Errorf("Username = %q, want %q", got.Username, tc.username)
			}
			if got.Password != tc.password {
				t.Errorf("Password = %q, want %q", got.Password, tc.password)
			}
			if got.DB != tc.db {
				t.Errorf("DB = %d, want %d", got.DB, tc.db)
			}
			if got.TLS != tc.tls {
				t.Errorf("TLS = %v, want %v", got.TLS, tc.tls)
			}
		})
	}
}

func TestParseRedisURLRejectsBadInput(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"postgres scheme", "postgres://localhost:5432/db"},
		{"http scheme", "http://localhost:6379"},
		{"missing host", "redis://"},
		{"no scheme at all", "localhost:6379"},
		{"control character in url", "redis://local\x7fhost:6379"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseRedisURL(tc.in); err == nil {
				t.Fatalf("expected an error for %q", tc.in)
			}
		})
	}
}

// The password must never reach stdout, a log line, or a report.
func TestRedactRedisURLHidesCredentials(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		secrets []string
	}{
		{
			// No username: url.Userinfo reports an empty username, so the
			// redaction collapses to a bare "***@".
			in:      "redis://:hunter2@cache.example.com:6380/0",
			want:    "redis://***@cache.example.com:6380/0",
			secrets: []string{"hunter2"},
		},
		{
			in:      "redis://app:s3cret@cache.example.com:6379",
			want:    "redis://app:***@cache.example.com:6379",
			secrets: []string{"s3cret"},
		},
		{
			in:   "rediss://cache.example.com:6380/2",
			want: "rediss://cache.example.com:6380/2",
		},
	}

	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := redactRedisURL(tc.in)
			if got != tc.want {
				t.Errorf("redactRedisURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
			for _, secret := range tc.secrets {
				if strings.Contains(got, secret) {
					t.Errorf("redacted url still contains %q: %s", secret, got)
				}
			}
		})
	}
}

// Redis answers `CONFIG GET dir dbfilename` with the parameters sorted by name,
// not in the requested order. Verified against redis-server 7.0.15:
//
//	> CONFIG GET dir dbfilename
//	1) dbfilename  2) dump.rdb  3) dir  4) /var/lib/redis
func TestParseConfigPairMatchesByNameNotPosition(t *testing.T) {
	cases := []struct {
		name  string
		reply redis.Value
		dir   string
		file  string
	}{
		{
			name:  "real redis ordering (alphabetical)",
			reply: []redis.Value{"dbfilename", "dump.rdb", "dir", "/var/lib/redis"},
			dir:   "/var/lib/redis",
			file:  "dump.rdb",
		},
		{
			name:  "requested ordering, in case that ever changes",
			reply: []redis.Value{"dir", "/var/lib/redis", "dbfilename", "dump.rdb"},
			dir:   "/var/lib/redis",
			file:  "dump.rdb",
		},
		{
			name:  "uppercase parameter names",
			reply: []redis.Value{"DIR", "/data", "DBFILENAME", "cache.rdb"},
			dir:   "/data",
			file:  "cache.rdb",
		},
		{
			name:  "extra parameters are ignored",
			reply: []redis.Value{"appendonly", "no", "dbfilename", "dump.rdb", "dir", "/data", "save", "3600"},
			dir:   "/data",
			file:  "dump.rdb",
		},
		{
			name:  "only one parameter present",
			reply: []redis.Value{"dir", "/data"},
			dir:   "/data",
			file:  "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir, file := parseConfigPair(tc.reply)
			if dir != tc.dir || file != tc.file {
				t.Fatalf("parseConfigPair = (%q, %q), want (%q, %q)", dir, file, tc.dir, tc.file)
			}
		})
	}
}

func TestParseConfigPairRejectsUnexpectedShapes(t *testing.T) {
	if dir, name := parseConfigPair(nil); dir != "" || name != "" {
		t.Errorf("nil reply should yield empty pair, got (%q, %q)", dir, name)
	}
	if dir, name := parseConfigPair("not an array"); dir != "" || name != "" {
		t.Errorf("scalar reply should yield empty pair, got (%q, %q)", dir, name)
	}
	// An odd-length reply has a trailing name with no value; it must not panic.
	if _, name := parseConfigPair([]redis.Value{"dir", "/data", "dbfilename"}); name != "" {
		t.Errorf("dangling name should be ignored, got %q", name)
	}
}

func TestIsNetworkError(t *testing.T) {
	if isNetworkError(nil) {
		t.Error("nil is not a network error")
	}
	for _, msg := range []string{
		"dial tcp 127.0.0.1:6379: connect: connection refused",
		"dial tcp: lookup cache: no such host",
		"dial tcp 10.0.0.1:6379: i/o timeout",
		"dial tcp: network is unreachable",
	} {
		if !isNetworkError(errors.New(msg)) {
			t.Errorf("expected %q to classify as a network error", msg)
		}
	}
	// A Redis -ERR reply is an auth/config problem, not an unreachable host.
	if isNetworkError(errors.New("redis: WRONGPASS invalid username-password pair")) {
		t.Error("a WRONGPASS reply must not be reported as unreachable")
	}
}

func TestFmtTtlAlwaysListsEveryBucket(t *testing.T) {
	got := fmtTtl(map[string]int64{"lt_1h": 3, "no_expiry": 1})
	want := "no_expiry=1 lt_1m=0 lt_1h=3 lt_1d=0 gt_1d=0"
	if got != want {
		t.Errorf("fmtTtl = %q, want %q", got, want)
	}
}
