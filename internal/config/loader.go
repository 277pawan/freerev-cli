// Package config turns revenant.yaml into Go structs the rest of the
// program can use. Nothing in this package talks to the database.
package config

import (
	"bytes"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// File is the top-level shape of revenant.yaml.
//
// YAML field names use snake_case (expect_tables). Go fields use CamelCase.
// The `yaml:"..."` tags are how yaml.v3 maps between the two.
type File struct {
	Plan     string   `yaml:"plan"`
	Database Database `yaml:"database"`
	Checks   []Check  `yaml:"checks"`
}

type Database struct {
	Engine     string `yaml:"engine"`     // phase 1: must be "postgres"
	Connection string `yaml:"connection"` // usually "${DATABASE_URL}"
}

// Check is a *union* of every check type we support.
// Unused fields stay empty depending on `type`.
//
//	type: connect      -> (no fields)
//	type: schema       -> ExpectTables
//	type: row_count    -> Table, Min, Max (optional)
//	type: foreign_key  -> Table, References
//	type: golden_query -> Query, ExpectMin
//	type: freshness    -> Table, Column, MaxAge
//	type: index        -> ExpectIndexes
type Check struct {
	Type          string   `yaml:"type"`
	ExpectTables  []string `yaml:"expect_tables,omitempty"`
	Table         string   `yaml:"table,omitempty"`
	Min           *int     `yaml:"min,omitempty"`
	Max           *int     `yaml:"max,omitempty"`
	References    string   `yaml:"references,omitempty"`
	Query         string   `yaml:"query,omitempty"`
	ExpectMin     *int     `yaml:"expect_min,omitempty"`
	Column        string   `yaml:"column,omitempty"`
	MaxAge        string   `yaml:"max_age,omitempty"`
	ExpectIndexes []string `yaml:"expect_indexes,omitempty"`
	// http_health
	URL          string `yaml:"url,omitempty"`
	Method       string `yaml:"method,omitempty"`
	ExpectStatus int    `yaml:"expect_status,omitempty"`
	Name         string `yaml:"name,omitempty"`
}

// Load reads path, unmarshals YAML, and expands ${ENV} in the connection string.
func Load(path string) (*File, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	var cfg File
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parse yaml %s: %w", path, err)
	}

	if cfg.Plan == "" {
		return nil, fmt.Errorf("revenant.yaml: plan is required")
	}
	if cfg.Database.Connection == "" {
		return nil, fmt.Errorf("revenant.yaml: database.connection is required")
	}
	if cfg.Database.Engine != "postgres" {
		return nil, fmt.Errorf("revenant-free supports only database.engine: postgres")
	}
	if len(cfg.Checks) == 0 {
		return nil, fmt.Errorf("revenant.yaml: at least one check is required")
	}
	if err := ValidateChecks(cfg.Checks); err != nil {
		return nil, err
	}

	var missing []string
	cfg.Database.Connection, missing = expandEnv(cfg.Database.Connection)
	for _, name := range missing {
		return nil, fmt.Errorf("database.connection references unset environment variable %q", name)
	}

	return &cfg, nil
}

// expandEnv supports the yaml style we document: ${DATABASE_URL}.
// Missing variables are retained so callers can produce a useful error.
func expandEnv(s string) (string, []string) {
	var missing []string
	expanded := os.Expand(s, func(name string) string {
		value, ok := os.LookupEnv(name)
		if !ok {
			missing = append(missing, name)
			return "${" + name + "}"
		}
		return value
	})
	return strings.TrimSpace(expanded), missing
}
