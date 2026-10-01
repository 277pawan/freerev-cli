package checks

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/pawan-bisht/revenant-free-cli/internal/config"
)

// fkPair is one column on the child table pointing at one column on the parent.
type fkPair struct {
	ChildColumn  string
	ParentColumn string
}

// ForeignKey proves two things about a restored database:
//
//  1. Postgres still has a FOREIGN KEY from item.Table → item.References
//     (schema survived the restore).
//  2. No orphan rows exist for that key (data survived intact).
//
// YAML:
//
//   - type: foreign_key
//     table: orders          # child
//     references: customers  # parent
//
// Phase 2 only supports a single-column FK. Composite keys can be added later
// by looping pairs instead of taking rows[0].
func ForeignKey(ctx context.Context, conn *pgx.Conn, item config.Check) ([]Result, error) {
	if item.Table == "" || item.References == "" {
		return nil, fmt.Errorf("foreign_key check needs table and references")
	}
	if err := SafeIdent(item.Table); err != nil {
		return nil, fmt.Errorf("foreign_key table: %w", err)
	}
	if err := SafeIdent(item.References); err != nil {
		return nil, fmt.Errorf("foreign_key references: %w", err)
	}

	name := fmt.Sprintf("foreign_key:%s->%s", item.Table, item.References)

	pairs, err := lookupFK(ctx, conn, item.Table, item.References)
	if err != nil {
		return nil, err
	}
	if len(pairs) == 0 {
		return []Result{{
			Name:    name,
			Status:  StatusFail,
			Message: fmt.Sprintf("no foreign key from %s to %s", item.Table, item.References),
		}}, nil
	}
	if len(pairs) > 1 {
		return nil, fmt.Errorf("foreign_key %s -> %s: composite/multiple keys are not supported yet", item.Table, item.References)
	}

	orphans, err := countOrphans(ctx, conn, item.Table, item.References, pairs[0])
	if err != nil {
		return nil, err
	}
	if orphans > 0 {
		return []Result{{
			Name:    name,
			Status:  StatusFail,
			Message: fmt.Sprintf("foreign key %s -> %s has %d orphan row(s)", item.Table, item.References, orphans),
		}}, nil
	}

	return []Result{{
		Name:    name,
		Status:  StatusPass,
		Message: fmt.Sprintf("foreign key %s -> %s intact", item.Table, item.References),
	}}, nil
}

func lookupFK(ctx context.Context, conn *pgx.Conn, child, parent string) ([]fkPair, error) {
	// Catalog-only query: does not touch user table data.
	const q = `
		SELECT
			kcu.column_name AS child_column,
			ccu.column_name AS parent_column
		FROM information_schema.table_constraints AS tc
		JOIN information_schema.key_column_usage AS kcu
		  ON tc.constraint_name = kcu.constraint_name
		 AND tc.table_schema = kcu.table_schema
		JOIN information_schema.referential_constraints AS rc
		  ON tc.constraint_name = rc.constraint_name
		 AND tc.table_schema = rc.constraint_schema
		JOIN information_schema.constraint_column_usage AS ccu
		  ON rc.unique_constraint_name = ccu.constraint_name
		 AND rc.unique_constraint_schema = ccu.constraint_schema
		WHERE tc.constraint_type = 'FOREIGN KEY'
		  AND tc.table_schema = 'public'
		  AND tc.table_name = $1
		  AND ccu.table_name = $2`

	rows, err := conn.Query(ctx, q, child, parent)
	if err != nil {
		return nil, fmt.Errorf("lookup foreign key %s -> %s: %w", child, parent, err)
	}
	defer rows.Close()

	var pairs []fkPair
	for rows.Next() {
		var p fkPair
		if err := rows.Scan(&p.ChildColumn, &p.ParentColumn); err != nil {
			return nil, err
		}
		pairs = append(pairs, p)
	}
	return pairs, rows.Err()
}

func countOrphans(ctx context.Context, conn *pgx.Conn, child, parent string, p fkPair) (int, error) {
	if err := SafeIdent(p.ChildColumn); err != nil {
		return 0, err
	}
	if err := SafeIdent(p.ParentColumn); err != nil {
		return 0, err
	}

	// NULL child values are not orphans (FK allows NULL unless the column is NOT NULL).
	q := fmt.Sprintf(
		`SELECT COUNT(*)
		 FROM %s AS child
		 LEFT JOIN %s AS parent ON child.%s = parent.%s
		 WHERE child.%s IS NOT NULL AND parent.%s IS NULL`,
		child, parent, p.ChildColumn, p.ParentColumn, p.ChildColumn, p.ParentColumn,
	)

	var n int
	if err := conn.QueryRow(ctx, q).Scan(&n); err != nil {
		return 0, fmt.Errorf("count orphans %s -> %s: %w", child, parent, err)
	}
	return n, nil
}
