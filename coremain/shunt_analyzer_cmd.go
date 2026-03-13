package coremain

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

func newRuntimeShuntCmd() *cobra.Command {
	ctx := &runtimeCmdContext{}
	var (
		domainName string
		qtype      string
		limit      int
	)

	cmd := &cobra.Command{
		Use:   "shunt",
		Short: "Inspect shunt rule matches and conflicts from the static config.",
	}
	cmd.PersistentFlags().StringVarP(&ctx.configPath, "config", "c", "", "config file used to resolve the config directory")
	cmd.PersistentFlags().StringVarP(&ctx.baseDir, "dir", "d", "", "config base directory, defaults to config directory or current working directory")

	explainCmd := &cobra.Command{
		Use:   "explain",
		Short: "Explain how a domain matches shunt rules and which path wins.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(domainName) == "" && len(args) > 0 {
				domainName = args[0]
			}
			if strings.TrimSpace(domainName) == "" {
				return fmt.Errorf("domain is required")
			}
			baseDir, err := resolveRuntimeCommandBaseDir(ctx.configPath, ctx.baseDir)
			if err != nil {
				return err
			}
			analyzer, err := newShuntAnalyzer(baseDir)
			if err != nil {
				return err
			}
			result, err := analyzer.Explain(domainName, qtype)
			if err != nil {
				return err
			}
			data, err := json.Marshal(result)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), string(data))
			return err
		},
		SilenceUsage: true,
		Args:         cobra.MaximumNArgs(1),
	}
	explainCmd.Flags().StringVar(&domainName, "domain", "", "domain name to analyze")
	explainCmd.Flags().StringVar(&qtype, "qtype", "A", "query type to explain, e.g. A or AAAA")

	conflictsCmd := &cobra.Command{
		Use:   "conflicts",
		Short: "List overlapping rule keys across shunt providers.",
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir, err := resolveRuntimeCommandBaseDir(ctx.configPath, ctx.baseDir)
			if err != nil {
				return err
			}
			analyzer, err := newShuntAnalyzer(baseDir)
			if err != nil {
				return err
			}
			conflicts := analyzer.Conflicts()
			if limit > 0 && len(conflicts) > limit {
				conflicts = conflicts[:limit]
			}
			payload := map[string]any{
				"base_dir":  filepath.Clean(baseDir),
				"count":     len(conflicts),
				"conflicts": conflicts,
				"warnings":  analyzer.warnings,
			}
			data, err := json.Marshal(payload)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), string(data))
			return err
		},
		SilenceUsage: true,
	}
	conflictsCmd.Flags().IntVar(&limit, "limit", 100, "maximum number of conflicts to print")

	cmd.AddCommand(explainCmd, conflictsCmd)
	return cmd
}
