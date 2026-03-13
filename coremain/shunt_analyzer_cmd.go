package coremain

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

func newRuntimeShuntCmd() *cobra.Command {
	ctx := &runtimeCmdContext{}
	var (
		domainName string
		qtype      string
		limit      int
		format     string
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
			switch strings.ToLower(strings.TrimSpace(format)) {
			case "", "json":
				data, err := json.Marshal(result)
				if err != nil {
					return err
				}
				_, err = fmt.Fprintln(cmd.OutOrStdout(), string(data))
				return err
			case "table":
				return writeShuntExplainTable(cmd.OutOrStdout(), result)
			default:
				return fmt.Errorf("unsupported format %q", format)
			}
		},
		SilenceUsage: true,
		Args:         cobra.MaximumNArgs(1),
	}
	explainCmd.Flags().StringVar(&domainName, "domain", "", "domain name to analyze")
	explainCmd.Flags().StringVar(&qtype, "qtype", "A", "query type to explain, e.g. A or AAAA")
	explainCmd.Flags().StringVar(&format, "format", "json", "output format: json or table")

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
			switch strings.ToLower(strings.TrimSpace(format)) {
			case "", "json":
				data, err := json.Marshal(payload)
				if err != nil {
					return err
				}
				_, err = fmt.Fprintln(cmd.OutOrStdout(), string(data))
				return err
			case "table":
				return writeShuntConflictsTable(cmd.OutOrStdout(), payload["base_dir"].(string), conflicts, analyzer.warnings)
			default:
				return fmt.Errorf("unsupported format %q", format)
			}
		},
		SilenceUsage: true,
	}
	conflictsCmd.Flags().IntVar(&limit, "limit", 100, "maximum number of conflicts to print")
	conflictsCmd.Flags().StringVar(&format, "format", "json", "output format: json or table")

	cmd.AddCommand(explainCmd, conflictsCmd)
	return cmd
}

func writeShuntExplainTable(w io.Writer, result *shuntExplainResult) error {
	if result == nil {
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintf(tw, "DOMAIN\t%s\nQTYPE\t%s\nDEFAULT\t%d (%s)\nDECISION\t%s\nREASON\t%s\n\n",
		result.Domain, result.QType, result.DefaultMark, result.DefaultTag, result.Decision.Action, result.Decision.Reason); err != nil {
		return err
	}
	if len(result.Matches) > 0 {
		if _, err := fmt.Fprintln(tw, "MATCHES"); err != nil {
			return err
		}
		if _, err := fmt.Fprintln(tw, "MARK\tTAG\tOUTPUT\tPLUGIN\tSOURCE_FILES"); err != nil {
			return err
		}
		for _, match := range result.Matches {
			if _, err := fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\n", match.Mark, match.Tag, match.OutputTag, match.PluginType, strings.Join(match.SourceFiles, ", ")); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(tw); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(tw, "DECISION_PATH"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(tw, "#\tSTAGE\tMARK\tTAG\tACTION\tMATCHED\tWINNER\tREASON"); err != nil {
		return err
	}
	for _, step := range result.DecisionPath {
		winner := ""
		if step.DecisionHit {
			winner = "yes"
		}
		if _, err := fmt.Fprintf(tw, "%d\t%s\t%d\t%s\t%s\t%t\t%s\t%s\n",
			step.Order, step.Stage, step.Mark, step.RuleTag, step.Action, step.Matched, winner, step.Reason); err != nil {
			return err
		}
	}
	if len(result.Warnings) > 0 {
		if _, err := fmt.Fprintln(tw); err != nil {
			return err
		}
		if _, err := fmt.Fprintln(tw, "WARNINGS"); err != nil {
			return err
		}
		for _, warning := range result.Warnings {
			if _, err := fmt.Fprintf(tw, "-\t%s\n", warning); err != nil {
				return err
			}
		}
	}
	return tw.Flush()
}

func writeShuntConflictsTable(w io.Writer, baseDir string, conflicts []shuntConflictEntry, warnings []string) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintf(tw, "BASE_DIR\t%s\nCOUNT\t%d\n\n", baseDir, len(conflicts)); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(tw, "RULE_KEY\tMARKS\tPROVIDERS"); err != nil {
		return err
	}
	for _, conflict := range conflicts {
		marks := make([]string, 0, len(conflict.Providers))
		providers := make([]string, 0, len(conflict.Providers))
		for _, provider := range conflict.Providers {
			marks = append(marks, fmt.Sprintf("%d", provider.Mark))
			providers = append(providers, fmt.Sprintf("%s(%s)", provider.Tag, provider.OutputTag))
		}
		sort.Strings(marks)
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\n", conflict.RuleKey, strings.Join(marks, ","), strings.Join(providers, ", ")); err != nil {
			return err
		}
	}
	if len(warnings) > 0 {
		if _, err := fmt.Fprintln(tw); err != nil {
			return err
		}
		if _, err := fmt.Fprintln(tw, "WARNINGS"); err != nil {
			return err
		}
		for _, warning := range warnings {
			if _, err := fmt.Fprintf(tw, "-\t%s\n", warning); err != nil {
				return err
			}
		}
	}
	return tw.Flush()
}
