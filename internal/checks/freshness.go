package checks

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pawan-bisht/revenant-free-cli/internal/config"
)

// Freshness proves the data in the table is not too old (RPO).
// It queries the table for the latest timestamp in Column and ensures it is within MaxAge.
func Freshness(ctx context.Context, conn *pgx.Conn, item config.Check) ([]Result, error) {
	if item.Table == "" {
		return nil, fmt.Errorf("freshness check needs table")
	}
	if item.Column == "" {
		return nil, fmt.Errorf("freshness check needs column")
	}
	if item.MaxAge == "" {
		return nil, fmt.Errorf("freshness check needs max_age")
	}
	if err := SafeIdent(item.Table); err != nil {
		return nil, err
	}
	if err := SafeIdent(item.Column); err != nil {
		return nil, err
	}

	maxAge, err := time.ParseDuration(item.MaxAge)
	if err != nil {
		return nil, fmt.Errorf("freshness check invalid max_age %q: %w", item.MaxAge, err)
	}

	q := fmt.Sprintf("SELECT %s FROM %s ORDER BY %s DESC LIMIT 1", item.Column, item.Table, item.Column)

	var latest time.Time
	if err := conn.QueryRow(ctx, q).Scan(&latest); err != nil {
		return nil, fmt.Errorf("freshness %s: %w", item.Table, err)
	}

	age := time.Since(latest)
	name := "freshness:" + item.Table

	if age <= maxAge {
		return []Result{{
			Name:    name,
			Status:  StatusPass,
			Message: fmt.Sprintf("%s data is fresh: %v <= %v", item.Table, age.Round(time.Second), maxAge),
		}}, nil
	}

	return []Result{{
		Name:    name,
		Status:  StatusFail,
		Message: fmt.Sprintf("%s data is too old: %v > %v", item.Table, age.Round(time.Second), maxAge),
	}}, nil
}
