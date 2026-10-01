// Package discover introspects a live Postgres catalog so `revenant init`
// can scaffold a starter yaml. It is not a check — it only reads
// information_schema (and COUNT(*) for baselines).
package discover

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/277pawan/freerev-cli/internal/checks"
)

// Snapshot is what we learned from the database.
type Snapshot struct {
	Schema string
	Tables []string
	Counts map[string]int
	FKs    []FK
}

// FK is a single-column foreign key we can put straight into yaml.
type FK struct {
	Table      string
	References string
}

// Schema reads public (or another schema) tables, row counts, and FKs.
func Schema(ctx context.Context, conn *pgx.Conn, schema string) (*Snapshot, error) {
	if err := checks.SafeIdent(schema); err != nil {
		return nil, fmt.Errorf("schema: %w", err)
	}

	tables, err := listTables(ctx, conn, schema)
	if err != nil {
		return nil, err
	}

	counts := make(map[string]int, len(tables))
	var usable []string
	for _, t := range tables {
		if err := checks.SafeIdent(t); err != nil {
			// Skip quoted/"weird" names — Phase 2 checks cannot splice them into SQL.
			continue
		}
		n, err := countRows(ctx, conn, t)
		if err != nil {
			return nil, err
		}
		usable = append(usable, t)
		counts[t] = n
	}

	fks, err := listFKs(ctx, conn, schema)
	if err != nil {
		return nil, err
	}

	return &Snapshot{
		Schema: schema,
		Tables: usable,
		Counts: counts,
		FKs:    fks,
	}, nil
}

func listTables(ctx context.Context, conn *pgx.Conn, schema string) ([]string, error) {
	const q = `
		SELECT table_name
		FROM information_schema.tables
		WHERE table_schema = $1
		  AND table_type = 'BASE TABLE'
		ORDER BY table_name`

	rows, err := conn.Query(ctx, q, schema)
	if err != nil {
		return nil, fmt.Errorf("list tables: %w", err)
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

func countRows(ctx context.Context, conn *pgx.Conn, table string) (int, error) {
	q := fmt.Sprintf("SELECT COUNT(*) FROM %s", table)
	var n int
	if err := conn.QueryRow(ctx, q).Scan(&n); err != nil {
		return 0, fmt.Errorf("count %s: %w", table, err)
	}
	return n, nil
}

func listFKs(ctx context.Context, conn *pgx.Conn, schema string) ([]FK, error) {
	// HAVING COUNT(*) = 1 drops composite keys (Phase 2 foreign_key cannot handle them).
	const q = `
		SELECT
			tc.table_name AS child_table,
			ccu.table_name AS parent_table
		FROM information_schema.table_constraints AS tc
		JOIN information_schema.referential_constraints AS rc
		  ON tc.constraint_name = rc.constraint_name
		 AND tc.table_schema = rc.constraint_schema
		JOIN information_schema.constraint_column_usage AS ccu
		  ON rc.unique_constraint_name = ccu.constraint_name
		 AND rc.unique_constraint_schema = ccu.constraint_schema
		WHERE tc.constraint_type = 'FOREIGN KEY'
		  AND tc.table_schema = $1
		GROUP BY tc.constraint_name, tc.table_name, ccu.table_name
		HAVING COUNT(*) = 1
		ORDER BY tc.table_name, ccu.table_name`

	rows, err := conn.Query(ctx, q, schema)
	if err != nil {
		return nil, fmt.Errorf("list foreign keys: %w", err)
	}
	defer rows.Close()

	var out []FK
	for rows.Next() {
		var fk FK
		if err := rows.Scan(&fk.Table, &fk.References); err != nil {
			return nil, err
		}
		if checks.SafeIdent(fk.Table) != nil || checks.SafeIdent(fk.References) != nil {
			continue
		}
		out = append(out, fk)
	}
	return out, rows.Err()
}
