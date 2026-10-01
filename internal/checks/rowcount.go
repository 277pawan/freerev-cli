package checks

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/277pawan/freerev-cli/internal/config"
)

// RowCount proves a table is not empty (or meets a minimum the user chose).
//
// Table names cannot be bound as $1 in PostgreSQL — only values can.
// We therefore validate the identifier ourselves, then splice it in.
func RowCount(ctx context.Context, conn *pgx.Conn, item config.Check) ([]Result, error) {
	if item.Table == "" {
		return nil, fmt.Errorf("row_count check needs table")
	}
	if item.Min == nil {
		return nil, fmt.Errorf("row_count check needs min")
	}
	if err := SafeIdent(item.Table); err != nil {
		return nil, err
	}

	q := fmt.Sprintf("SELECT COUNT(*) FROM %s", item.Table)

	var count int
	if err := conn.QueryRow(ctx, q).Scan(&count); err != nil {
		return nil, fmt.Errorf("count %s: %w", item.Table, err)
	}

	name := "row_count:" + item.Table
	if count < *item.Min {
		return []Result{{
			Name:    name,
			Status:  StatusFail,
			Message: fmt.Sprintf("%s row count %d < min %d", item.Table, count, *item.Min),
		}}, nil
	}
	if item.Max != nil && count > *item.Max {
		return []Result{{
			Name:    name,
			Status:  StatusFail,
			Message: fmt.Sprintf("%s row count %d > max %d", item.Table, count, *item.Max),
		}}, nil
	}

	msg := fmt.Sprintf("%s row count %d >= %d", item.Table, count, *item.Min)
	if item.Max != nil {
		msg = fmt.Sprintf("%s row count %d within [%d, %d]", item.Table, count, *item.Min, *item.Max)
	}
	return []Result{{
		Name:    name,
		Status:  StatusPass,
		Message: msg,
	}}, nil
}
