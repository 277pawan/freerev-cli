package cmd

import (
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "revenant-free",
	Short: "Run local PostgreSQL checks and create validation reports",
	Long: `Revenant Free connects directly to a PostgreSQL database and runs the
checks defined in revenant.yaml. It does not create, restore, or manage database
backups or cloud resources.`,
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
