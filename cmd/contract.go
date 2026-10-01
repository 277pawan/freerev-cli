package cmd

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

var contractPath string

var contractCmd = &cobra.Command{Use: "contract", Short: "Validate a recovery contract"}
var contractValidateCmd = &cobra.Command{
	Use:          "validate",
	Short:        "Validate recovery contract YAML",
	SilenceUsage: true,
	RunE:         runContractValidate,
}

var contractDurationPattern = regexp.MustCompile(`(?i)^\d+(\.\d+)?(ms|s|m|h|d)$`)

func init() {
	rootCmd.AddCommand(contractCmd)
	contractCmd.AddCommand(contractValidateCmd)
	contractValidateCmd.Flags().StringVarP(&contractPath, "file", "f", "recovery-contract.yaml", "contract YAML path")
}

func runContractValidate(_ *cobra.Command, _ []string) error {
	raw, err := os.ReadFile(contractPath)
	if err != nil {
		return err
	}
	var doc map[string]interface{}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("parse yaml: %w", err)
	}
	version, _ := doc["version"].(string)
	if strings.TrimSpace(version) == "" {
		return fmt.Errorf("recovery contract: version is required")
	}
	recovery, ok := doc["recovery"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("recovery contract: recovery block is required")
	}
	rto, _ := recovery["rto"].(string)
	rpo, _ := recovery["rpo"].(string)
	if !contractDurationPattern.MatchString(strings.TrimSpace(rto)) {
		return fmt.Errorf("recovery.rto must be a duration like 15m")
	}
	if !contractDurationPattern.MatchString(strings.TrimSpace(rpo)) {
		return fmt.Errorf("recovery.rpo must be a duration like 5m")
	}
	fmt.Printf("Recovery contract valid (version %s, RTO %s, RPO %s)\n", version, rto, rpo)
	return nil
}
