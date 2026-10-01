// Package report writes the durable evidence file for a verify run.
//
// JSON is the machine-readable report. Markdown is in markdown.go.
// Prometheus is still out of scope (paid/later). Keep Report the single source of truth.
package report

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/pawan-bisht/revenant-free-cli/internal/checks"
)

const (
	StatusPass = "PASS"
	StatusFail = "FAIL"
)

type Report struct {
	Plan     string          `json:"plan"`
	Status   string          `json:"status"`
	Duration string          `json:"duration"`
	Checks   []checks.Result `json:"checks"`
}

func Build(plan string, results []checks.Result, d time.Duration) Report {
	status := StatusPass
	for _, r := range results {
		if r.Status != checks.StatusPass {
			status = StatusFail
			break
		}
	}

	return Report{
		Plan:     plan,
		Status:   status,
		Duration: d.Round(time.Millisecond).String(),
		Checks:   results,
	}
}

func WriteJSON(path string, r Report) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal report: %w", err)
	}
	data = append(data, '\n')

	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
