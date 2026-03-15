package coremain

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newConfigCmd() *cobra.Command {
	var inputPath string

	configCmd := &cobra.Command{
		Use:   "config",
		Short: "Manage mosdns configuration files.",
	}

	validateCmd := &cobra.Command{
		Use:   "validate",
		Short: "Validate the current config file after v2 compilation.",
		RunE: func(cmd *cobra.Command, args []string) error {
			_, fileUsed, err := loadConfig(inputPath)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "config is valid: %s\n", fileUsed)
			return err
		},
		SilenceUsage: true,
	}
	validateCmd.Flags().StringVarP(&inputPath, "config", "c", "", "config file, defaults to auto-discovered config")
	configCmd.AddCommand(validateCmd)

	return configCmd
}
