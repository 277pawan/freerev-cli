package cmd

import (
	"fmt"
	"time"

	"github.com/joho/godotenv"
	"github.com/spf13/cobra"

	"github.com/pawan-bisht/revenant-free-cli/internal/checks"
	"github.com/pawan-bisht/revenant-free-cli/internal/config"
	"github.com/pawan-bisht/revenant-free-cli/internal/database"
	"github.com/pawan-bisht/revenant-free-cli/internal/report"
)

var (
	verifyConfig   string
	verifyPlan     string
	verifyJSON     string
	verifyMarkdown string
)

var verifyCmd = &cobra.Command{
	Use:          "verify",
	Short:        "Run checks against a local or directly reachable PostgreSQL database",
	SilenceUsage: true,
	RunE:         runVerify,
}

func init() {
	rootCmd.AddCommand(verifyCmd)
	verifyCmd.Flags().StringVarP(&verifyConfig, "config", "c", "revenant.yaml", "path to revenant.yaml")
	verifyCmd.Flags().StringVar(&verifyPlan, "plan", "", "optional plan name; must match the config")
	verifyCmd.Flags().StringVarP(&verifyJSON, "output", "o", "report.json", "JSON report path")
	verifyCmd.Flags().StringVar(&verifyMarkdown, "markdown", "report.md", "Markdown report path")
}

func runVerify(cmd *cobra.Command, _ []string) error {
	_ = godotenv.Load()
	cfg, err := config.Load(verifyConfig)
	if err != nil {
		return err
	}
	if verifyPlan != "" && verifyPlan != cfg.Plan {
		return fmt.Errorf("--plan %q does not match config plan %q", verifyPlan, cfg.Plan)
	}

	started := time.Now()
	db, err := database.ConnectURL(cfg.Database.Connection)
	if err != nil {
		return err
	}
	defer db.Close(cmd.Context())

	results, err := checks.RunAll(db.Conn(), cfg.Checks)
	if err != nil {
		return err
	}
	httpResults, err := checks.RunHTTP(cfg.Checks)
	if err != nil {
		return err
	}
	results = append(results, httpResults...)

	passed := 0
	for _, result := range results {
		mark := "✗"
		if result.Status == checks.StatusPass {
			mark = "✓"
			passed++
		}
		fmt.Printf("%s %s", mark, result.Name)
		if result.Message != "" {
			fmt.Printf(" — %s", result.Message)
		}
		fmt.Println()
	}

	rep := report.Build(cfg.Plan, results, time.Since(started))
	if err := report.WriteJSON(verifyJSON, rep); err != nil {
		return err
	}
	if err := report.WriteMarkdown(verifyMarkdown, rep); err != nil {
		return err
	}
	fmt.Printf("\nValidation: %s (%d/%d checks passed)\n", rep.Status, passed, len(results))
	fmt.Printf("Duration: %s\nWrote %s and %s\n", rep.Duration, verifyJSON, verifyMarkdown)
	if rep.Status != report.StatusPass {
		return fmt.Errorf("one or more checks failed")
	}
	return nil
}
