package checks

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/277pawan/freerev-cli/internal/config"
)

// Golden runs a user-written SQL query and checks the first column of the
// first row against expect_min.
//
// This is the check Revenant can never invent for you — business rules like
// "yesterday's orders should exist" live here.
//
// YAML:
//
//   - type: golden_query
//     query: "SELECT count(*) FROM orders"
//     expect_min: 1
//
// The query is executed as-is (the user owns it). We only refuse empty SQL
// and extra statements after a semicolon, so one yaml check cannot run a
// batch of commands.
func Golden(ctx context.Context, conn *pgx.Conn, item config.Check) ([]Result, error) {
	query := strings.TrimSpace(item.Query)
	if query == "" {
		return nil, fmt.Errorf("golden_query check needs query")
	}
	if item.ExpectMin == nil {
		return nil, fmt.Errorf("golden_query check needs expect_min")
	}
	if err := singleStatement(query); err != nil {
		return nil, err
	}

	var value int64
	if err := conn.QueryRow(ctx, query).Scan(&value); err != nil {
		return []Result{{
			Name:    "golden_query",
			Status:  StatusFail,
			Message: fmt.Sprintf("golden query failed: %v", err),
		}}, nil
	}

	min := int64(*item.ExpectMin)
	if value >= min {
		return []Result{{
			Name:    "golden_query",
			Status:  StatusPass,
			Message: fmt.Sprintf("golden query returned %d >= %d", value, min),
		}}, nil
	}

	return []Result{{
		Name:    "golden_query",
		Status:  StatusFail,
		Message: fmt.Sprintf("golden query returned %d < expect_min %d", value, min),
	}}, nil
}

func singleStatement(query string) error {
	trimmed := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(query), ";"))
	if strings.Contains(trimmed, ";") {
		return fmt.Errorf("golden_query must be a single SQL statement")
	}
	return nil
}
