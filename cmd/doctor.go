package cmd

import (
	"fmt"

	"github.com/joho/godotenv"
	"github.com/spf13/cobra"

	"github.com/pawan-bisht/revenant-free-cli/internal/config"
	"github.com/pawan-bisht/revenant-free-cli/internal/database"
)

var doctorConfig string

var doctorCmd = &cobra.Command{
	Use:          "doctor",
	Short:        "Check config and PostgreSQL connectivity",
	SilenceUsage: true,
	RunE:         runDoctor,
}

func init() {
	rootCmd.AddCommand(doctorCmd)
	doctorCmd.Flags().StringVarP(&doctorConfig, "config", "c", "revenant.yaml", "path to revenant.yaml")
}

func runDoctor(cmd *cobra.Command, _ []string) error {
	_ = godotenv.Load()
	cfg, err := config.Load(doctorConfig)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	fmt.Printf("✓ Config loaded (plan=%q, %d checks)\n", cfg.Plan, len(cfg.Checks))
	db, err := database.ConnectURL(cfg.Database.Connection)
	if err != nil {
		return fmt.Errorf("database connection failed: %w", err)
	}
	defer db.Close(cmd.Context())
	fmt.Println("✓ PostgreSQL connection successful")
	fmt.Println("Doctor: PASS")
	return nil
}
