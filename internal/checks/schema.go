// Package checks
package checks

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/pawan-bisht/revenant-free-cli/internal/config"
)

// Schema proves the restored database still has the tables the user named.
//
// We ask information_schema (Postgres's built-in catalog), not the app
// source code. One yaml check can list many tables; we emit one Result
// per table so the report is easy to read.
func Schema(ctx context.Context, conn *pgx.Conn, item config.Check) ([]Result, error) {
	if len(item.ExpectTables) == 0 {
		return nil, fmt.Errorf("schema check needs expect_tables")
	}

	results := make([]Result, 0, len(item.ExpectTables))
	for _, table := range item.ExpectTables {
		exists, err := tableExists(ctx, conn, table)
		if err != nil {
			return nil, err
		}
		if exists {
			results = append(results, Result{
				Name:    "schema:" + table,
				Status:  StatusPass,
				Message: table + " table exists",
			})
			continue
		}
		results = append(results, Result{
			Name:    "schema:" + table,
			Status:  StatusFail,
			Message: table + " table missing",
		})
	}
	return results, nil
}

func tableExists(ctx context.Context, conn *pgx.Conn, table string) (bool, error) {
	// table_schema = 'public' is the default place CREATE TABLE puts things.
	// If you later need other schemas, add a yaml field `schema: sales`.
	const q = `
		SELECT EXISTS (
			SELECT 1
			FROM information_schema.tables
			WHERE table_schema = 'public'
			  AND table_name = $1
		)`

	var exists bool
	if err := conn.QueryRow(ctx, q, table).Scan(&exists); err != nil {
		return false, fmt.Errorf("lookup table %q: %w", table, err)
	}
	return exists, nil
}
