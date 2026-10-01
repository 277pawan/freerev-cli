package checks

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/pawan-bisht/revenant-free-cli/internal/config"
)

// Index proves named indexes exist in the public schema.
func Index(ctx context.Context, conn *pgx.Conn, item config.Check) ([]Result, error) {
	if len(item.ExpectIndexes) == 0 {
		return nil, fmt.Errorf("index check needs expect_indexes")
	}

	results := make([]Result, 0, len(item.ExpectIndexes))
	for _, name := range item.ExpectIndexes {
		if err := SafeIdent(name); err != nil {
			return nil, fmt.Errorf("index name: %w", err)
		}
		exists, err := indexExists(ctx, conn, name)
		if err != nil {
			return nil, err
		}
		if exists {
			results = append(results, Result{
				Name:    "index:" + name,
				Status:  StatusPass,
				Message: name + " index exists",
			})
			continue
		}
		results = append(results, Result{
			Name:    "index:" + name,
			Status:  StatusFail,
			Message: name + " index missing",
		})
	}
	return results, nil
}

func indexExists(ctx context.Context, conn *pgx.Conn, name string) (bool, error) {
	const q = `
		SELECT EXISTS (
			SELECT 1
			FROM pg_indexes
			WHERE schemaname = 'public'
			  AND indexname = $1
		)`
	var exists bool
	if err := conn.QueryRow(ctx, q, name).Scan(&exists); err != nil {
		return false, fmt.Errorf("lookup index %q: %w", name, err)
	}
	return exists, nil
}
