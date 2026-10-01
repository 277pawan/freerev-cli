package report

import (
	"fmt"
	"os"
	"strings"
)

// WriteMarkdown writes a human-readable copy of the same Report as JSON.
// Auditors can open this file; machines should keep using report.json.
func WriteMarkdown(path string, r Report) error {
	var b strings.Builder

	fmt.Fprintf(&b, "# Revenant validation report\n\n")
	fmt.Fprintf(&b, "- **Plan:** %s\n", r.Plan)
	fmt.Fprintf(&b, "- **Status:** %s\n", r.Status)
	fmt.Fprintf(&b, "- **Duration:** %s\n\n", r.Duration)
	fmt.Fprintf(&b, "| Check | Status | Detail |\n")
	fmt.Fprintf(&b, "|-------|--------|--------|\n")
	for _, c := range r.Checks {
		fmt.Fprintf(&b, "| %s | %s | %s |\n", c.Name, c.Status, c.Message)
	}

	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
