package coremain

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/IrineSistiana/mosdns/v5/pkg/rulesource"
)

var invalidRuleSourceIDChars = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

func loadRuleSourcesConfigAtPath(path string, scope rulesource.Scope) (rulesource.Config, bool, error) {
	cfg, exists, err := rulesource.LoadConfig(path, scope)
	if err != nil {
		return rulesource.Config{}, false, err
	}
	needsBootstrap, err := shouldBootstrapRuleSources(path, scope, exists, cfg)
	if err != nil || !needsBootstrap {
		return cfg, exists, err
	}

	bootstrapCfg, ok, err := bootstrapRuleSourcesFromFilesystem(path, scope)
	if err != nil || !ok {
		return cfg, exists, err
	}
	if err := saveBootstrappedRuleSources(path, scope, bootstrapCfg); err != nil {
		return rulesource.Config{}, false, err
	}
	return bootstrapCfg, true, nil
}

func shouldBootstrapRuleSources(
	path string,
	scope rulesource.Scope,
	exists bool,
	cfg rulesource.Config,
) (bool, error) {
	if scope != rulesource.ScopeAdguard {
		return false, nil
	}
	if !exists {
		return true, nil
	}
	if len(cfg.Sources) > 0 {
		return false, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("read rule source config %s: %w", path, err)
	}
	return isCommentOnlyRuleSourceConfig(raw), nil
}

func isCommentOnlyRuleSourceConfig(raw []byte) bool {
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		return false
	}
	return true
}

func bootstrapRuleSourcesFromFilesystem(path string, scope rulesource.Scope) (rulesource.Config, bool, error) {
	if scope != rulesource.ScopeAdguard {
		return rulesource.Config{}, false, nil
	}
	baseDir, ok := inferBaseDirFromRuleSourceConfigPath(path)
	if !ok {
		return rulesource.Config{}, false, nil
	}
	dir := filepath.Join(baseDir, string(scope))
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return rulesource.Config{}, false, nil
		}
		return rulesource.Config{}, false, fmt.Errorf("read bootstrap rule source dir %s: %w", dir, err)
	}

	sources := make([]rulesource.Source, 0, len(entries))
	seenIDs := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		relPath := filepath.ToSlash(filepath.Join(string(scope), entry.Name()))
		src, ok, err := buildBootstrappedAdguardSource(filepath.Join(dir, entry.Name()), relPath, seenIDs)
		if err != nil {
			return rulesource.Config{}, false, err
		}
		if !ok {
			continue
		}
		seenIDs[src.ID] = struct{}{}
		sources = append(sources, src)
	}
	if len(sources) == 0 {
		return rulesource.Config{}, false, nil
	}
	return rulesource.NormalizeConfig(rulesource.Config{Sources: sources}), true, nil
}

func inferBaseDirFromRuleSourceConfigPath(path string) (string, bool) {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "" || path == "." {
		return "", false
	}
	dir := filepath.Dir(path)
	if filepath.Base(dir) != customConfigDirname {
		return "", false
	}
	return filepath.Dir(dir), true
}

func buildBootstrappedAdguardSource(
	absPath string,
	relPath string,
	seenIDs map[string]struct{},
) (rulesource.Source, bool, error) {
	format, ok := detectRuleSourceFormatFromPath(relPath)
	if !ok {
		return rulesource.Source{}, false, nil
	}
	behavior, matchMode, err := inferAdguardSourceMode(absPath, format)
	if err != nil {
		return rulesource.Source{}, false, err
	}
	id := uniqueBootstrappedRuleSourceID(filepath.Base(relPath), seenIDs)
	source := rulesource.Source{
		ID:         id,
		Name:       id,
		Enabled:    true,
		Behavior:   behavior,
		MatchMode:  matchMode,
		Format:     format,
		SourceKind: rulesource.SourceKindLocal,
		Path:       relPath,
	}
	if err := rulesource.ValidateSource(rulesource.ScopeAdguard, source); err != nil {
		return rulesource.Source{}, false, fmt.Errorf("bootstrap adguard source %s: %w", relPath, err)
	}
	return source, true, nil
}

func detectRuleSourceFormatFromPath(path string) (rulesource.Format, bool) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".txt":
		return rulesource.FormatTXT, true
	case ".list":
		return rulesource.FormatList, true
	case ".rules":
		return rulesource.FormatRules, true
	case ".json":
		return rulesource.FormatJSON, true
	case ".yaml", ".yml":
		return rulesource.FormatYAML, true
	case ".srs":
		return rulesource.FormatSRS, true
	case ".mrs":
		return rulesource.FormatMRS, true
	default:
		return "", false
	}
}

func inferAdguardSourceMode(path string, format rulesource.Format) (rulesource.Behavior, rulesource.MatchMode, error) {
	switch format {
	case rulesource.FormatSRS, rulesource.FormatMRS:
		return rulesource.BehaviorDomain, rulesource.MatchModeDomainSet, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", fmt.Errorf("read adguard bootstrap source %s: %w", path, err)
	}
	if result, err := rulesource.ParseAdguardBytes(format, data); err == nil && result.Count() > 0 {
		return rulesource.BehaviorAdguard, rulesource.MatchModeAdguardNative, nil
	}
	if rules, err := rulesource.ParseDomainBytes(format, data); err == nil && len(rules) > 0 {
		return rulesource.BehaviorDomain, rulesource.MatchModeDomainSet, nil
	}
	return rulesource.BehaviorAdguard, rulesource.MatchModeAdguardNative, nil
}

func uniqueBootstrappedRuleSourceID(name string, seen map[string]struct{}) string {
	stem := strings.TrimSuffix(name, filepath.Ext(name))
	id := sanitizeBootstrappedRuleSourceID(stem)
	if _, exists := seen[id]; !exists {
		return id
	}
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s_%d", id, i)
		if _, exists := seen[candidate]; !exists {
			return candidate
		}
	}
}

func sanitizeBootstrappedRuleSourceID(value string) string {
	value = invalidRuleSourceIDChars.ReplaceAllString(strings.TrimSpace(value), "_")
	value = strings.Trim(value, "_-")
	if value == "" {
		return "adguard_source"
	}
	first := value[0]
	if !(first >= 'A' && first <= 'Z' || first >= 'a' && first <= 'z' || first >= '0' && first <= '9') {
		return "src_" + value
	}
	return value
}

func saveBootstrappedRuleSources(path string, scope rulesource.Scope, cfg rulesource.Config) error {
	switch scope {
	case rulesource.ScopeAdguard:
		return SaveAdguardSourcesToPath(path, cfg)
	case rulesource.ScopeDiversion:
		return SaveDiversionSourcesToPath(path, cfg)
	default:
		return fmt.Errorf("unsupported scope %q", scope)
	}
}
