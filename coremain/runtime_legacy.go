package coremain

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

type LegacyRuntimeImportSummary struct {
	Overrides int `json:"overrides"`
	Upstreams int `json:"upstreams"`
	Switches  int `json:"switches"`
	Webinfo   int `json:"webinfo"`
	Requery   int `json:"requery"`
}

func ImportLegacyRuntimeState(baseDir string) (LegacyRuntimeImportSummary, error) {
	baseDir = strings.TrimSpace(baseDir)
	if baseDir == "" {
		return LegacyRuntimeImportSummary{}, fmt.Errorf("base dir is required")
	}
	absBaseDir, err := filepath.Abs(baseDir)
	if err == nil {
		baseDir = absBaseDir
	}

	summary := LegacyRuntimeImportSummary{}

	if err := importLegacyOverrides(baseDir, &summary); err != nil {
		return summary, err
	}
	if err := importLegacyUpstreams(baseDir, &summary); err != nil {
		return summary, err
	}
	if err := importLegacyRecursiveFiles(baseDir, &summary); err != nil {
		return summary, err
	}
	return summary, nil
}

func importLegacyOverrides(baseDir string, summary *LegacyRuntimeImportSummary) error {
	path := filepath.Join(baseDir, overridesFilename)
	var payload GlobalOverrides
	ok, err := readJSONFile(path, &payload)
	if err != nil {
		return fmt.Errorf("import legacy overrides: %w", err)
	}
	if !ok {
		return nil
	}
	dbPath := filepath.Join(baseDir, runtimeStateDBFilename)
	if err := SaveRuntimeStateJSONToPath(dbPath, runtimeStateNamespaceOverrides, runtimeStateKeyGlobalOverrides, payload); err != nil {
		return err
	}
	summary.Overrides++
	return nil
}

func importLegacyUpstreams(baseDir string, summary *LegacyRuntimeImportSummary) error {
	path := filepath.Join(baseDir, upstreamOverridesFilename)
	var payload GlobalUpstreamOverrides
	ok, err := readJSONFile(path, &payload)
	if err != nil {
		return fmt.Errorf("import legacy upstreams: %w", err)
	}
	if !ok {
		return nil
	}
	dbPath := filepath.Join(baseDir, runtimeStateDBFilename)
	if err := SaveRuntimeStateJSONToPath(dbPath, runtimeStateNamespaceUpstreams, runtimeStateKeyUpstreamConfig, payload); err != nil {
		return err
	}
	summary.Upstreams++
	return nil
}

func importLegacyRecursiveFiles(baseDir string, summary *LegacyRuntimeImportSummary) error {
	return filepath.WalkDir(baseDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		switch {
		case name == "switches.json":
			return importLegacySwitchFile(path, summary)
		case name == "requeryconfig.json":
			return importLegacyRequeryConfig(path, summary)
		case name == "requeryconfig.state.json":
			return importLegacyRequeryState(path, summary)
		case strings.HasSuffix(strings.ToLower(name), ".json") && strings.Contains(filepath.ToSlash(path), "/webinfo/"):
			return importLegacyWebinfoFile(path, summary)
		default:
			return nil
		}
	})
}

func importLegacySwitchFile(path string, summary *LegacyRuntimeImportSummary) error {
	dbPath := filepath.Join(filepath.Dir(filepath.Clean(path)), runtimeStateDBFilename)
	var payload map[string]string
	ok, err := readJSONFile(path, &payload)
	if err != nil {
		return fmt.Errorf("import legacy switch file %s: %w", path, err)
	}
	if !ok {
		return nil
	}
	if err := SaveRuntimeStateJSONToPath(dbPath, runtimeNamespaceSwitch, filepath.Clean(path), payload); err != nil {
		return err
	}
	summary.Switches++
	return nil
}

func importLegacyWebinfoFile(path string, summary *LegacyRuntimeImportSummary) error {
	if strings.Contains(strings.ToLower(path), "requeryconfig") {
		return nil
	}
	dbPath := filepath.Join(filepath.Dir(filepath.Clean(path)), runtimeStateDBFilename)
	var payload any
	ok, err := readJSONFile(path, &payload)
	if err != nil {
		return fmt.Errorf("import legacy webinfo file %s: %w", path, err)
	}
	if !ok {
		return nil
	}
	if err := SaveRuntimeStateJSONToPath(dbPath, runtimeNamespaceWebinfo, filepath.Clean(path), payload); err != nil {
		return err
	}
	summary.Webinfo++
	return nil
}

func importLegacyRequeryConfig(path string, summary *LegacyRuntimeImportSummary) error {
	raw, ok, err := readJSONFileToRawMap(path)
	if err != nil {
		return fmt.Errorf("import legacy requery config %s: %w", path, err)
	}
	if !ok {
		return nil
	}
	dbPath := filepath.Join(filepath.Dir(filepath.Clean(path)), runtimeStateDBFilename)
	configKey := filepath.Clean(path) + ":config"
	stateKey := filepath.Clean(legacyRequeryStatePath(path)) + ":state"

	statePayload := map[string]json.RawMessage{}
	if value, ok := raw["status"]; ok {
		statePayload["status"] = value
		delete(raw, "status")
	}
	if value, ok := raw["full_rebuild_task"]; ok {
		statePayload["full_rebuild_task"] = value
		delete(raw, "full_rebuild_task")
	}

	if err := SaveRuntimeStateJSONToPath(dbPath, runtimeNamespaceRequery, configKey, raw); err != nil {
		return err
	}
	if len(statePayload) > 0 {
		if err := SaveRuntimeStateJSONToPath(dbPath, runtimeNamespaceRequery, stateKey, statePayload); err != nil {
			return err
		}
	}
	summary.Requery++
	return nil
}

func importLegacyRequeryState(path string, summary *LegacyRuntimeImportSummary) error {
	var payload any
	ok, err := readJSONFile(path, &payload)
	if err != nil {
		return fmt.Errorf("import legacy requery state %s: %w", path, err)
	}
	if !ok {
		return nil
	}
	dbPath := filepath.Join(filepath.Dir(filepath.Clean(path)), runtimeStateDBFilename)
	stateKey := filepath.Clean(path) + ":state"
	if err := SaveRuntimeStateJSONToPath(dbPath, runtimeNamespaceRequery, stateKey, payload); err != nil {
		return err
	}
	summary.Requery++
	return nil
}

func legacyRequeryStatePath(configPath string) string {
	ext := filepath.Ext(configPath)
	if ext == "" {
		return configPath + ".state.json"
	}
	return strings.TrimSuffix(configPath, ext) + ".state" + ext
}

func readJSONFile(path string, dst any) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return false, nil
	}
	if err := json.Unmarshal(data, dst); err != nil {
		return false, err
	}
	return true, nil
}

func readJSONFileToRawMap(path string) (map[string]json.RawMessage, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, false, nil
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, false, err
	}
	return payload, true, nil
}
