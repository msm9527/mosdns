package coremain

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/spf13/cobra"
)

type runtimeCmdContext struct {
	configPath string
	baseDir    string
	limit      int
}

func newRuntimeCmd() *cobra.Command {
	ctx := &runtimeCmdContext{}

	runtimeCmd := &cobra.Command{
		Use:   "runtime",
		Short: "Inspect and export mosdns runtime state stored in SQLite.",
	}
	runtimeCmd.PersistentFlags().StringVarP(&ctx.configPath, "config", "c", "", "config file used to resolve the runtime database directory")
	runtimeCmd.PersistentFlags().StringVarP(&ctx.baseDir, "dir", "d", "", "runtime base directory, defaults to config directory or current working directory")

	summaryCmd := &cobra.Command{
		Use:   "summary",
		Short: "Print runtime namespace summary as JSON.",
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir, err := resolveRuntimeCommandBaseDir(ctx.configPath, ctx.baseDir)
			if err != nil {
				return err
			}
			data, err := runtimeSummaryJSON(filepath.Join(baseDir, runtimeStateDBFilename))
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), string(data))
			return err
		},
		SilenceUsage: true,
	}
	runtimeCmd.AddCommand(summaryCmd)

	datasetsCmd := &cobra.Command{
		Use:   "datasets",
		Short: "List and export generated datasets stored in runtime SQLite.",
	}
	datasetsListCmd := &cobra.Command{
		Use:   "list",
		Short: "List generated datasets as JSON.",
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir, err := resolveRuntimeCommandBaseDir(ctx.configPath, ctx.baseDir)
			if err != nil {
				return err
			}
			data, err := runtimeDatasetsJSON(filepath.Join(baseDir, runtimeStateDBFilename))
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), string(data))
			return err
		},
		SilenceUsage: true,
	}
	datasetsExportCmd := &cobra.Command{
		Use:   "export",
		Short: "Export generated datasets from SQLite back to files.",
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir, err := resolveRuntimeCommandBaseDir(ctx.configPath, ctx.baseDir)
			if err != nil {
				return err
			}
			exported, err := ExportGeneratedDatasetsToFiles(filepath.Join(baseDir, runtimeStateDBFilename))
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "exported_files=%d\n", exported)
			return err
		},
		SilenceUsage: true,
	}
	datasetsCmd.AddCommand(datasetsListCmd, datasetsExportCmd)
	runtimeCmd.AddCommand(datasetsCmd)

	eventsCmd := &cobra.Command{
		Use:   "events",
		Short: "List recent runtime system events as JSON.",
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir, err := resolveRuntimeCommandBaseDir(ctx.configPath, ctx.baseDir)
			if err != nil {
				return err
			}
			data, err := runtimeEventsJSON(filepath.Join(baseDir, runtimeStateDBFilename), ctx.limit)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), string(data))
			return err
		},
		SilenceUsage: true,
	}
	eventsCmd.Flags().IntVar(&ctx.limit, "limit", 20, "number of events to print")
	runtimeCmd.AddCommand(eventsCmd)

	return runtimeCmd
}

func resolveRuntimeCommandBaseDir(configPath, baseDir string) (string, error) {
	if baseDir != "" {
		if abs, err := filepath.Abs(baseDir); err == nil {
			return abs, nil
		}
		return baseDir, nil
	}
	if configPath != "" {
		_, fileUsed, err := loadConfig(configPath)
		if err != nil {
			return "", err
		}
		return resolveBaseDir(fileUsed), nil
	}
	if MainConfigBaseDir != "" {
		return MainConfigBaseDir, nil
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return wd, nil
}

func runtimeSummaryJSON(dbPath string) ([]byte, error) {
	namespaces := []string{
		runtimeStateNamespaceOverrides,
		runtimeStateNamespaceUpstreams,
		runtimeNamespaceSwitch,
		runtimeNamespaceWebinfo,
		runtimeNamespaceRequery,
		runtimeStateNamespaceGeneratedDataset,
	}
	resp := runtimeSummaryResponse{
		StorageEngine: "sqlite",
		DBPath:        dbPath,
		Namespaces:    make([]runtimeNamespaceSummary, 0, len(namespaces)),
	}
	for _, namespace := range namespaces {
		entries, err := ListRuntimeStateByNamespace(dbPath, namespace)
		if err != nil {
			return nil, err
		}
		resp.Namespaces = append(resp.Namespaces, runtimeNamespaceSummary{
			Namespace: namespace,
			Keys:      len(entries),
		})
	}
	return json.Marshal(resp)
}

func runtimeDatasetsJSON(dbPath string) ([]byte, error) {
	datasets, err := ListGeneratedDatasetsFromPath(dbPath)
	if err != nil {
		return nil, err
	}
	return json.Marshal(datasets)
}

func runtimeEventsJSON(dbPath string, limit int) ([]byte, error) {
	events, err := ListSystemEvents(dbPath, "", limit)
	if err != nil {
		return nil, err
	}
	return json.Marshal(events)
}

func parseExportedFilesOutput(s string) (int, error) {
	const prefix = "exported_files="
	if len(s) < len(prefix) || s[:len(prefix)] != prefix {
		return 0, fmt.Errorf("unexpected export output %q", s)
	}
	value := s[len(prefix):]
	if len(value) > 0 && value[len(value)-1] == '\n' {
		value = value[:len(value)-1]
	}
	return strconv.Atoi(value)
}
