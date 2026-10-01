package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadRejectsUnsupportedRecoveryConfiguration(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost:5432/app?sslmode=disable")
	path := filepath.Join(t.TempDir(), "revenant.yaml")
	content := `plan: local
database:
  engine: postgres
  connection: ${DATABASE_URL}
recovery:
  provider: remote
checks:
  - type: connect
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "field recovery not found") {
		t.Fatalf("Load() error = %v, want recovery field rejection", err)
	}
}

func TestValidateChecksRejectsTypo(t *testing.T) {
	err := ValidateChecks([]Check{{Type: "rowcount", Table: "orders", Min: ptr(1)}})
	if err == nil {
		t.Fatal("expected error for rowcount typo")
	}
}

func TestValidateChecksRowCountMaxLessThanMin(t *testing.T) {
	err := ValidateChecks([]Check{{Type: "row_count", Table: "orders", Min: ptr(10), Max: ptr(5)}})
	if err == nil {
		t.Fatal("expected error when max < min")
	}
}

func ptr(n int) *int { return &n }
