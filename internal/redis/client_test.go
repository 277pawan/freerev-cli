package redis

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// ─── helpers ───────────────────────────────────────────────────────────────

// fakeServer speaks just enough RESP to exercise the client. handler receives
// each decoded command as its args and returns the raw bytes to reply with.
func fakeServer(t *testing.T, handler func(args []string) string) (addr string, stop func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go serveFake(conn, handler)
		}
	}()
	return ln.Addr().String(), func() { _ = ln.Close() }
}

func serveFake(conn net.Conn, handler func(args []string) string) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	for {
		args, err := readCommand(r)
		if err != nil {
			return
		}
		reply := handler(args)
		if _, err := io.WriteString(conn, reply); err != nil {
			return
		}
	}
}

// readCommand decodes one client->server command (the array form Do writes).
func readCommand(r *bufio.Reader) ([]string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	if len(line) == 0 || line[0] != '*' {
		return nil, errors.New("not a command array")
	}
	var n int
	if _, err := fmt.Sscanf(line[1:], "%d", &n); err != nil {
		return nil, err
	}
	args := make([]string, 0, n)
	for i := 0; i < n; i++ {
		head, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		var blen int
		if _, err := fmt.Sscanf(head[1:], "%d", &blen); err != nil {
			return nil, err
		}
		buf := make([]byte, blen+2)
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, err
		}
		args = append(args, string(buf[:blen]))
	}
	return args, nil
}

// ─── reply decoding ────────────────────────────────────────────────────────

func TestReadReplyDecodesRESP2Types(t *testing.T) {
	cases := []struct {
		name  string
		wire  string
		want  Value
		isErr bool
	}{
		{"simple string", "+OK\r\n", "OK", false},
		{"error", "-ERR unknown command\r\n", nil, true},
		{"integer", ":6379\r\n", int64(6379), false},
		{"bulk string", "$5\r\nhello\r\n", "hello", false},
		{"empty bulk", "$0\r\n\r\n", "", false},
		{"null bulk", "$-1\r\n", nil, false},
		{"array", "*2\r\n$3\r\nfoo\r\n:7\r\n", []Value{"foo", int64(7)}, false},
		{"empty array", "*0\r\n", []Value{}, false},
		{"null array", "*-1\r\n", nil, false},
		{"unknown prefix", "!boom\r\n", nil, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := readReply(bufio.NewReader(strings.NewReader(tc.wire)))
			if tc.isErr {
				if err == nil {
					t.Fatalf("expected error, got %#v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if fmt.Sprintf("%#v", got) != fmt.Sprintf("%#v", tc.want) {
				t.Fatalf("got %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestReadReplyReturnsTypedRedisError(t *testing.T) {
	_, err := readReply(bufio.NewReader(strings.NewReader("-WRONGPASS invalid credentials\r\n")))
	if err == nil {
		t.Fatal("expected an error")
	}
	var rerr *Error
	if !errors.As(err, &rerr) {
		t.Fatalf("want *redis.Error so auth failures are distinguishable, got %T", err)
	}
	if !strings.Contains(rerr.Msg, "WRONGPASS") {
		t.Fatalf("error message lost the server text: %q", rerr.Msg)
	}
}

// An error nested inside an array must not abort decoding the rest.
func TestReadReplyKeepsNestedErrors(t *testing.T) {
	wire := "*2\r\n-ERR bad\r\n$3\r\nfoo\r\n"
	got, err := readReply(bufio.NewReader(strings.NewReader(wire)))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	arr, ok := got.([]Value)
	if !ok || len(arr) != 2 {
		t.Fatalf("want 2-element array, got %#v", got)
	}
	if _, ok := arr[0].(*Error); !ok {
		t.Fatalf("first element should be *Error, got %T", arr[0])
	}
	if arr[1] != "foo" {
		t.Fatalf("decoding stopped early: %#v", arr[1])
	}
}

// ─── auth handshake ────────────────────────────────────────────────────────

func TestDialAuthenticatesWithUsernameAndPassword(t *testing.T) {
	var got []string
	addr, stop := fakeServer(t, func(args []string) string {
		got = append(got, strings.Join(args, " "))
		return "+OK\r\n"
	})
	defer stop()

	c, err := Dial(Options{Addr: addr, Username: "app", Password: "s3cret", Timeout: time.Second})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	if len(got) != 1 || got[0] != "AUTH app s3cret" {
		t.Fatalf("expected a single AUTH <user> <pass>, got %v", got)
	}
}

// Redis 5 does not know ACL usernames: the client must fall back to the legacy
// single-argument AUTH rather than failing the connection.
func TestDialFallsBackToLegacyAuth(t *testing.T) {
	var got []string
	addr, stop := fakeServer(t, func(args []string) string {
		joined := strings.Join(args, " ")
		got = append(got, joined)
		if strings.HasPrefix(joined, "AUTH app ") {
			return "-ERR unknown user 'app'\r\n"
		}
		return "+OK\r\n"
	})
	defer stop()

	c, err := Dial(Options{Addr: addr, Username: "app", Password: "s3cret", Timeout: time.Second})
	if err != nil {
		t.Fatalf("expected fallback to legacy AUTH, got: %v", err)
	}
	defer c.Close()

	if len(got) != 2 || got[1] != "AUTH s3cret" {
		t.Fatalf("expected legacy AUTH fallback, got %v", got)
	}
}

func TestDialSelectsNonDefaultDB(t *testing.T) {
	var got []string
	addr, stop := fakeServer(t, func(args []string) string {
		got = append(got, strings.Join(args, " "))
		return "+OK\r\n"
	})
	defer stop()

	c, err := Dial(Options{Addr: addr, DB: 3, Timeout: time.Second})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	if len(got) != 1 || got[0] != "SELECT 3" {
		t.Fatalf("expected SELECT 3, got %v", got)
	}
}

// DB 0 is the default and must not emit a SELECT.
func TestDialSkipsSelectForDefaultDB(t *testing.T) {
	var got []string
	addr, stop := fakeServer(t, func(args []string) string {
		got = append(got, strings.Join(args, " "))
		return "+OK\r\n"
	})
	defer stop()

	c, err := Dial(Options{Addr: addr, Timeout: time.Second})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	if len(got) != 0 {
		t.Fatalf("expected no commands for db 0, got %v", got)
	}
}

// A wrong password must surface as a Redis error, not as "unreachable".
func TestDialSurfacesWrongPasswordAsRedisError(t *testing.T) {
	addr, stop := fakeServer(t, func(args []string) string {
		return "-WRONGPASS invalid username-password pair\r\n"
	})
	defer stop()

	_, err := Dial(Options{Addr: addr, Password: "nope", Timeout: time.Second})
	if err == nil {
		t.Fatal("expected an auth failure")
	}
	var rerr *Error
	if !errors.As(err, &rerr) {
		t.Fatalf("want *redis.Error, got %T: %v", err, err)
	}
}

func TestDialRequiresAddr(t *testing.T) {
	if _, err := Dial(Options{}); err == nil {
		t.Fatal("expected an error for an empty address")
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	addr, stop := fakeServer(t, func([]string) string { return "+OK\r\n" })
	defer stop()

	c, err := Dial(Options{Addr: addr, Timeout: time.Second})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("second close should be a no-op, got %v", err)
	}
}

// ─── INFO / keyspace parsing ───────────────────────────────────────────────

func TestInfoParsesKeyValueLines(t *testing.T) {
	raw := "# Server\r\nredis_version:7.0.15\r\nos:Linux\r\n\r\n# Persistence\r\nrdb_last_bgsave_status:ok\r\n"
	info := Info(raw)

	if info["redis_version"] != "7.0.15" {
		t.Errorf("redis_version = %q", info["redis_version"])
	}
	if info["rdb_last_bgsave_status"] != "ok" {
		t.Errorf("rdb_last_bgsave_status = %q", info["rdb_last_bgsave_status"])
	}
	if _, leaked := info["# Server"]; leaked {
		t.Error("comment lines must not become keys")
	}
}

func TestParseKeyspace(t *testing.T) {
	info := map[string]string{
		"keyspace": "db0:keys=42,expires=7,avg_ttl=0\r\ndb3:keys=5,expires=0,avg_ttl=0",
	}
	got := ParseKeyspace(info)
	if len(got) != 2 {
		t.Fatalf("want 2 databases, got %#v", got)
	}
	if got[0].DB != "db0" || got[0].Keys != 42 || got[0].Expires != 7 {
		t.Errorf("db0 = %#v", got[0])
	}
	if got[1].DB != "db3" || got[1].Keys != 5 {
		t.Errorf("db3 = %#v", got[1])
	}
}

func TestTextAndIntCoercion(t *testing.T) {
	if Text(nil) != "" {
		t.Error("nil should coerce to empty string")
	}
	if Text(int64(42)) != "42" {
		t.Error("int64 should coerce to its decimal form")
	}
	if Text([]Value{"a", "b"}) != "a b" {
		t.Error("array should join with spaces")
	}
	if Int("17") != 17 {
		t.Error("string should coerce to int")
	}
	if Int(nil) != 0 {
		t.Error("nil should coerce to 0")
	}
}
