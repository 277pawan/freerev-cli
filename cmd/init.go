package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/joho/godotenv"
	"github.com/spf13/cobra"

	"github.com/277pawan/freerev-cli/internal/database"
	"github.com/277pawan/freerev-cli/internal/discover"
)

var (
	initOutput string
	initPlan   string
	initSchema string
	initForce  bool
)

var initCmd = &cobra.Command{
	Use:          "init",
	Short:        "Inspect PostgreSQL and scaffold a starter revenant.yaml",
	SilenceUsage: true,
	RunE:         runInit,
}

func init() {
	rootCmd.AddCommand(initCmd)
	initCmd.Flags().StringVarP(&initOutput, "output", "o", "revenant.yaml", "config output path")
	initCmd.Flags().StringVar(&initPlan, "plan", "local-demo", "plan name")
	initCmd.Flags().StringVar(&initSchema, "schema", "public", "PostgreSQL schema to inspect")
	initCmd.Flags().BoolVar(&initForce, "force", false, "overwrite an existing config")
}

func runInit(cmd *cobra.Command, _ []string) error {
	_ = godotenv.Load()
	connection := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if connection == "" {
		return fmt.Errorf("DATABASE_URL is not set; export it or add it to .env")
	}
	if !initForce {
		if _, err := os.Stat(initOutput); err == nil {
			return fmt.Errorf("%s already exists (pass --force to overwrite)", initOutput)
		}
	}
	db, err := database.ConnectURL(connection)
	if err != nil {
		return err
	}
	defer db.Close(cmd.Context())

	snapshot, err := discover.Schema(cmd.Context(), db.Conn(), initSchema)
	if err != nil {
		return err
	}
	body, err := discover.YAML(initPlan, snapshot)
	if err != nil {
		return err
	}
	if err := os.WriteFile(initOutput, []byte(body), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", initOutput, err)
	}
	fmt.Printf("Found %d table(s) in %s: %s\nWrote %s\n", len(snapshot.Tables), snapshot.Schema, strings.Join(snapshot.Tables, ", "), initOutput)
	fmt.Printf("Next: review the checks, then run `revenant-free verify -c %s`\n", initOutput)
	return nil
}
