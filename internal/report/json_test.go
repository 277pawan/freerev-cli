package report

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/277pawan/freerev-cli/internal/checks"
)

func TestReportIncludesLocalValidationResults(t *testing.T) {
	report := Report{
		Plan:   "production",
		Status: StatusPass,
		Checks: []checks.Result{{Name: "connect", Status: StatusPass}},
	}

	data, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	for _, field := range []string{`"plan":"production"`, `"status":"PASS"`, `"name":"connect"`} {
		if !strings.Contains(string(data), field) {
			t.Errorf("serialized report missing %s: %s", field, data)
		}
	}
}
