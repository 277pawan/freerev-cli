// Package database is the *only* place that knows about pgx.
//
// Checks receive a *pgx.Conn and run SQL. If we later add MySQL, you
// introduce an interface here — you do not sprinkle pgx calls through checks.
package database

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

const connectTimeout = 10 * time.Second

// Connection wraps pgx.Conn for clean resource management
type Connection struct {
	conn *pgx.Conn
}

// Connect opens a single Postgres connection using a connection string.
//
// Deprecated: Use ConnectURL instead (same function, clearer name).
func Connect(connectionString string) (*pgx.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
	defer cancel()

	conn, err := pgx.Connect(ctx, connectionString)
	if err != nil {
		return nil, fmt.Errorf("postgres connect: %w", err)
	}

	if err := conn.Ping(ctx); err != nil {
		conn.Close(ctx)
		return nil, fmt.Errorf("postgres ping: %w", err)
	}

	return conn, nil
}

// ConnectURL opens a single Postgres connection using a connection string.
//
// Phase 1 uses one connection (plenty for a handful of checks). A pool
// (pgxpool) is only worth it if we later need concurrent database queries.
func ConnectURL(connectionString string) (*Connection, error) {
	ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
	defer cancel()

	conn, err := pgx.Connect(ctx, connectionString)
	if err != nil {
		return nil, fmt.Errorf("postgres connect: %w", err)
	}

	if err := conn.Ping(ctx); err != nil {
		conn.Close(ctx)
		return nil, fmt.Errorf("postgres ping: %w", err)
	}

	return &Connection{conn: conn}, nil
}

// Conn returns the underlying pgx.Conn for use by checks
func (c *Connection) Conn() *pgx.Conn {
	return c.conn
}

// Close closes the connection
func (c *Connection) Close(ctx context.Context) error {
	return c.conn.Close(ctx)
}
