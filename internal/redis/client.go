// Package redis is a minimal RESP client used by Revenant's Redis provider.
//
// Why not a Redis library: the provider needs eight read-only commands plus
// AUTH/SELECT, and the free Developer plan runs this binary on a customer's
// own machine. A dependency-free client keeps that install small and keeps
// the credential-handling path auditable.
//
// Deliberate limits — this is not a general-purpose Redis driver:
//   - no pipelining, no transactions, no Pub/Sub
//   - no automatic reconnect (a reconnect would silently mask a broken backup)
//   - always Close(), ideally via With()
package redis

import (
	"bufio"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

// Value is one decoded RESP reply: nil, int64, string, []Value, or Error.
type Value any

// Error is a Redis `-ERR ...` reply. Distinct from a transport failure so a
// wrong password is never reported as "unreachable".
type Error struct{ Msg string }

func (e *Error) Error() string { return "redis: " + e.Msg }

// Options mirrors the fields of a redis:// or rediss:// URL.
type Options struct {
	Addr     string
	Username string
	Password string
	DB       int
	TLS      bool
	Timeout  time.Duration
}

// Client is a single synchronous connection. Not safe for concurrent use.
type Client struct {
	conn net.Conn
	r    *bufio.Reader
	w    *bufio.Writer
	// closed makes Close idempotent: net.Conn.Close reports "use of closed
	// network connection" on a second call, which would contradict the
	// contract callers like the restore cleanup rely on.
	closed bool
}

// Dial opens a connection, authenticates and selects the database.
func Dial(opts Options) (*Client, error) {
	if opts.Addr == "" {
		return nil, errors.New("redis: address is required")
	}
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = 5 * time.Second
	}

	var (
		conn net.Conn
		err  error
	)
	dialer := &net.Dialer{Timeout: timeout}
	if opts.TLS {
		conn, err = tls.DialWithDialer(dialer, "tcp", opts.Addr, &tls.Config{})
	} else {
		conn, err = dialer.Dial("tcp", opts.Addr)
	}
	if err != nil {
		return nil, err
	}

	c := &Client{
		conn: conn,
		r:    bufio.NewReader(conn),
		w:    bufio.NewWriter(conn),
	}

	if opts.Password != "" {
		// Redis 6+ wants AUTH <user> <pass>; Redis 5 only understands AUTH <pass>.
		// A Redis *error* reply here means the ACL user is unknown — fall back
		// to the legacy form. A transport error is fatal.
		if opts.Username != "" {
			if _, err := c.Do("AUTH", opts.Username, opts.Password); err == nil {
				return c.selectDB(opts.DB, timeout)
			} else {
				var rerr *Error
				if !errors.As(err, &rerr) {
					_ = c.Close()
					return nil, err
				}
			}
		}
		if _, err := c.Do("AUTH", opts.Password); err != nil {
			_ = c.Close()
			return nil, err
		}
	}
	return c.selectDB(opts.DB, timeout)
}

func (c *Client) selectDB(db int, timeout time.Duration) (*Client, error) {
	if db > 0 {
		if _, err := c.Do("SELECT", strconv.Itoa(db)); err != nil {
			_ = c.Close()
			return nil, err
		}
	}
	_ = c.conn.SetDeadline(time.Now().Add(timeout))
	return c, nil
}

// Close releases the connection. Safe to call twice.
func (c *Client) Close() error {
	if c == nil || c.conn == nil || c.closed {
		return nil
	}
	c.closed = true
	return c.conn.Close()
}

// Do sends one command and returns the decoded reply.
func (c *Client) Do(args ...string) (Value, error) {
	if len(args) == 0 {
		return nil, errors.New("redis: empty command")
	}
	if err := c.conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return nil, err
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "*%d\r\n", len(args))
	for _, a := range args {
		fmt.Fprintf(&sb, "$%d\r\n%s\r\n", len(a), a)
	}
	if _, err := c.w.WriteString(sb.String()); err != nil {
		return nil, err
	}
	if err := c.w.Flush(); err != nil {
		return nil, err
	}
	return readReply(c.r)
}

func readReply(r *bufio.Reader) (Value, error) {
	prefix, err := r.ReadByte()
	if err != nil {
		return nil, err
	}
	line, err := readLine(r)
	if err != nil {
		return nil, err
	}

	switch prefix {
	case '+':
		return line, nil
	case '-':
		return nil, &Error{Msg: line}
	case ':':
		return strconv.ParseInt(line, 10, 64)
	case '$':
		n, err := strconv.Atoi(line)
		if err != nil {
			return nil, fmt.Errorf("redis: bad bulk length %q", line)
		}
		if n == -1 {
			return nil, nil
		}
		buf := make([]byte, n+2) // payload + CRLF
		if _, err := ioReadFull(r, buf); err != nil {
			return nil, err
		}
		return string(buf[:n]), nil
	case '*':
		n, err := strconv.Atoi(line)
		if err != nil {
			return nil, fmt.Errorf("redis: bad array length %q", line)
		}
		if n == -1 {
			return nil, nil
		}
		out := make([]Value, 0, n)
		for i := 0; i < n; i++ {
			v, err := readReply(r)
			if err != nil {
				var rerr *Error
				if errors.As(err, &rerr) {
					// An error nested in an array (e.g. EXEC) should not abort
					// decoding the rest of the reply.
					out = append(out, rerr)
					continue
				}
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("redis: unknown reply prefix %q", string(prefix))
	}
}

func readLine(r *bufio.Reader) (string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func ioReadFull(r *bufio.Reader, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := r.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// ─── helpers ───────────────────────────────────────────────────────────────

// Text coerces a reply to a string.
func Text(v Value) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case int64:
		return strconv.FormatInt(t, 10)
	case []Value:
		parts := make([]string, 0, len(t))
		for _, item := range t {
			parts = append(parts, Text(item))
		}
		return strings.Join(parts, " ")
	default:
		return fmt.Sprintf("%v", t)
	}
}

// Int coerces a reply to an int64.
func Int(v Value) int64 {
	switch t := v.(type) {
	case int64:
		return t
	case string:
		n, _ := strconv.ParseInt(t, 10, 64)
		return n
	default:
		return 0
	}
}

// Info parses `INFO` output into a flat key→value map.
func Info(raw string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if idx := strings.Index(line, ":"); idx > 0 {
			out[line[:idx]] = line[idx+1:]
		}
	}
	return out
}

// Keyspace parses the `keyspace` section: "db0:keys=42,expires=7,avg_ttl=0".
type KeyspaceEntry struct {
	DB      string
	Keys    int64
	Expires int64
	AvgTTL  int64
}

func ParseKeyspace(info map[string]string) []KeyspaceEntry {
	var out []KeyspaceEntry
	for _, line := range strings.Split(info["keyspace"], "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var e KeyspaceEntry
		if !strings.HasPrefix(line, "db") {
			continue
		}
		colon := strings.Index(line, ":")
		if colon == -1 {
			continue
		}
		e.DB = line[:colon]
		for _, kv := range strings.Split(line[colon+1:], ",") {
			parts := strings.SplitN(kv, "=", 2)
			if len(parts) != 2 {
				continue
			}
			n, _ := strconv.ParseInt(parts[1], 10, 64)
			switch parts[0] {
			case "keys":
				e.Keys = n
			case "expires":
				e.Expires = n
			case "avg_ttl":
				e.AvgTTL = n
			}
		}
		out = append(out, e)
	}
	return out
}
