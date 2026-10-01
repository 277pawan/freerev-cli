package checks

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/277pawan/freerev-cli/internal/config"
)

// Connect proves the database accepts connections before heavier checks run.
func Connect(ctx context.Context, conn *pgx.Conn, _ config.Check) ([]Result, error) {
	if err := conn.Ping(ctx); err != nil {
		return []Result{{
			Name:    "connect",
			Status:  StatusFail,
			Message: "database ping failed: " + err.Error(),
		}}, nil
	}
	return []Result{{
		Name:    "connect",
		Status:  StatusPass,
		Message: "database connection OK",
	}}, nil
}
