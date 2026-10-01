package discover

import (
	"strings"
	"testing"
)

func TestYAMLIncludesDiscoveredChecks(t *testing.T) {
	snap := &Snapshot{
		Schema: "public",
		Tables: []string{"customers", "orders"},
		Counts: map[string]int{"customers": 1, "orders": 3},
		FKs:    []FK{{Table: "orders", References: "customers"}},
	}

	got, err := YAML("local-demo", snap)
	if err != nil {
		t.Fatal(err)
	}

	needles := []string{
		"plan: local-demo",
		"expect_tables:",
		"- customers",
		"- orders",
		"type: row_count",
		"table: orders",
		"min: 3",
		"type: foreign_key",
		"references: customers",
		"type: golden_query",
		"SELECT count(*) FROM orders",
	}
	for _, n := range needles {
		if !strings.Contains(got, n) {
			t.Errorf("generated yaml missing %q\n%s", n, got)
		}
	}
}

func TestYAMLRequiresTables(t *testing.T) {
	_, err := YAML("x", &Snapshot{Schema: "public"})
	if err == nil {
		t.Fatal("expected error for empty snapshot")
	}
}
