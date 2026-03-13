package coremain

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/IrineSistiana/mosdns/v5/internal/configv2"
	"github.com/spf13/cobra"
)

func newConfigCmd() *cobra.Command {
	var (
		inputPath       string
		outputPath      string
		stdout          bool
		pureDeclarative bool
	)

	configCmd := &cobra.Command{
		Use:   "config",
		Short: "Manage mosdns configuration files.",
	}

	migrateCmd := &cobra.Command{
		Use:   "migrate",
		Short: "Migrate a v1 config file into config v2.",
		RunE: func(cmd *cobra.Command, args []string) error {
			data, resolvedOutput, err := migrateConfigToV2(inputPath, outputPath, pureDeclarative)
			if err != nil {
				return err
			}
			if stdout {
				_, err = cmd.OutOrStdout().Write(data)
				return err
			}
			if err := os.WriteFile(resolvedOutput, data, 0644); err != nil {
				return fmt.Errorf("write migrated config: %w", err)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "migrated config written to %s\n", resolvedOutput)
			return err
		},
		SilenceUsage: true,
	}
	migrateCmd.Flags().StringVarP(&inputPath, "config", "c", "", "source config file, defaults to auto-discovered config")
	migrateCmd.Flags().StringVarP(&outputPath, "output", "o", "", "output path, defaults to <config>.v2.yaml")
	migrateCmd.Flags().BoolVar(&stdout, "stdout", false, "print migrated config to stdout instead of writing a file")
	migrateCmd.Flags().BoolVar(&pureDeclarative, "pure-declarative", false, "omit the legacy compatibility block and emit only the minimal declarative v2 subset")
	configCmd.AddCommand(migrateCmd)

	validateCmd := &cobra.Command{
		Use:   "validate",
		Short: "Validate the current config file after v1/v2 compilation.",
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

func migrateConfigToV2(inputPath, outputPath string, pureDeclarative bool) ([]byte, string, error) {
	v, raw, fileUsed, err := resolveConfigInput(inputPath)
	if err != nil {
		return nil, "", err
	}

	isV2, err := isConfigV2Document(raw)
	if err != nil {
		return nil, "", err
	}
	if isV2 {
		if outputPath == "" {
			outputPath = defaultMigratedConfigPath(fileUsed)
		}
		return raw, outputPath, nil
	}

	v1, err := decodeV1CompatConfig(v)
	if err != nil {
		return nil, "", err
	}
	cfgV2, err := configv2.MigrateV1ToV2(v1)
	if err != nil {
		return nil, "", err
	}
	if pureDeclarative {
		cfgV2.Legacy = configv2.LegacyConfig{}
	}
	data, err := configv2.Marshal(cfgV2)
	if err != nil {
		return nil, "", err
	}

	if outputPath == "" {
		outputPath = defaultMigratedConfigPath(fileUsed)
	}
	return data, outputPath, nil
}

func defaultMigratedConfigPath(fileUsed string) string {
	ext := filepath.Ext(fileUsed)
	if ext == "" {
		return fileUsed + ".v2.yaml"
	}
	return fileUsed[:len(fileUsed)-len(ext)] + ".v2" + ext
}
