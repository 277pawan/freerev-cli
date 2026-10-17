package cmd

import (
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "revenant-free",
	Short: "Run local PostgreSQL checks and prove Redis backups restore",
	Long: `Revenant Free connects directly to a PostgreSQL database and runs the
checks defined in revenant.yaml. The redis-prove command additionally proves
that a self-hosted Redis backup really restores. Neither creates, restores, or
manages backups or cloud resources.`,
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
