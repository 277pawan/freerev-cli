package config

import (
	"fmt"
	"strings"
)

var checkTypeHints = map[string]string{
	"rowcount":     "row_count",
	"row-count":    "row_count",
	"foreignkey":   "foreign_key",
	"foreign-key":  "foreign_key",
	"goldenquery":  "golden_query",
	"golden-query": "golden_query",
	"tables":       "schema (use type: schema with expect_tables)",
}

// ValidateChecks returns clear errors for unknown types and missing required fields.
func ValidateChecks(checks []Check) error {
	if len(checks) == 0 {
		return fmt.Errorf("revenant.yaml: at least one check is required")
	}

	for i, item := range checks {
		typ := strings.TrimSpace(item.Type)
		if typ == "" {
			return fmt.Errorf("checks[%d]: type is required", i)
		}

		if hint, ok := checkTypeHints[strings.ToLower(typ)]; ok {
			return fmt.Errorf("checks[%d]: unknown type %q — did you mean %q?", i, typ, hint)
		}

		switch typ {
		case "connect":
			// no fields
		case "schema":
			if len(item.ExpectTables) == 0 {
				return fmt.Errorf("checks[%d] type=schema: expect_tables is required", i)
			}
		case "row_count":
			if item.Table == "" {
				return fmt.Errorf("checks[%d] type=row_count: table is required", i)
			}
			if item.Min == nil {
				return fmt.Errorf("checks[%d] type=row_count: min is required", i)
			}
			if item.Max != nil && *item.Max < *item.Min {
				return fmt.Errorf("checks[%d] type=row_count: max must be >= min", i)
			}
		case "foreign_key":
			if item.Table == "" || item.References == "" {
				return fmt.Errorf("checks[%d] type=foreign_key: table and references are required", i)
			}
		case "golden_query":
			if strings.TrimSpace(item.Query) == "" {
				return fmt.Errorf("checks[%d] type=golden_query: query is required", i)
			}
			if item.ExpectMin == nil {
				return fmt.Errorf("checks[%d] type=golden_query: expect_min is required", i)
			}
		case "freshness":
			if item.Table == "" || item.Column == "" || item.MaxAge == "" {
				return fmt.Errorf("checks[%d] type=freshness: table, column, and max_age are required", i)
			}
		case "index":
			if len(item.ExpectIndexes) == 0 {
				return fmt.Errorf("checks[%d] type=index: expect_indexes is required", i)
			}
		case "http_health":
			if strings.TrimSpace(item.URL) == "" {
				return fmt.Errorf("checks[%d] type=http_health: url is required", i)
			}
		default:
			return fmt.Errorf("checks[%d]: unknown type %q (supported: connect, schema, row_count, foreign_key, golden_query, freshness, index, http_health)", i, typ)
		}
	}
	return nil
}
