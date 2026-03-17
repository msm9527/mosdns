package coremain

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/IrineSistiana/mosdns/v5/pkg/rulesource"
)

type RuleSourceSyncResult struct {
	Data        []byte
	LocalPath   string
	RuleCount   int
	LastUpdated time.Time
}

func LoadRuleSourceByID(configPath string, scope rulesource.Scope, sourceID string) (rulesource.Source, error) {
	cfg, _, err := rulesource.LoadConfig(ResolveMainConfigPath(configPath), scope)
	if err != nil {
		return rulesource.Source{}, err
	}
	for _, source := range cfg.Sources {
		if source.ID == sourceID {
			return source, nil
		}
	}
	return rulesource.Source{}, fmt.Errorf("source %q not found in %s", sourceID, configPath)
}

func SyncRuleSource(
	ctx context.Context,
	client *http.Client,
	dbPath string,
	baseDir string,
	scope rulesource.Scope,
	source rulesource.Source,
	forceRemote bool,
) (*RuleSourceSyncResult, error) {
	localPath, err := rulesource.ResolveLocalPath(baseDir, scope, source)
	if err != nil {
		return nil, err
	}
	if source.SourceKind == rulesource.SourceKindRemote && shouldDownloadRuleSource(dbPath, scope, source, localPath, forceRemote) {
		return downloadRuleSource(ctx, client, dbPath, scope, source, localPath)
	}
	return loadExistingRuleSource(dbPath, scope, source, localPath)
}

func shouldDownloadRuleSource(
	dbPath string,
	scope rulesource.Scope,
	source rulesource.Source,
	localPath string,
	forceRemote bool,
) bool {
	if forceRemote {
		return true
	}
	if _, err := os.Stat(localPath); err != nil {
		return true
	}
	if !source.AutoUpdate || source.UpdateIntervalHours < 1 {
		return false
	}
	statuses, err := ListRuleSourceStatusByScope(dbPath, scope)
	if err != nil {
		return true
	}
	status := statuses[source.ID]
	if status.LastUpdated.IsZero() {
		return true
	}
	interval := time.Duration(source.UpdateIntervalHours) * time.Hour
	return time.Since(status.LastUpdated) >= interval
}

func downloadRuleSource(
	ctx context.Context,
	client *http.Client,
	dbPath string,
	scope rulesource.Scope,
	source rulesource.Source,
	localPath string,
) (*RuleSourceSyncResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source.URL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		saveRuleSourceError(dbPath, scope, source.ID, err)
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		err := fmt.Errorf("download %s failed with status %d", source.ID, resp.StatusCode)
		saveRuleSourceError(dbPath, scope, source.ID, err)
		return nil, err
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		saveRuleSourceError(dbPath, scope, source.ID, err)
		return nil, err
	}
	count, err := parseRuleSourceCount(source, data)
	if err != nil {
		saveRuleSourceError(dbPath, scope, source.ID, err)
		return nil, err
	}
	if err := writeRuleSourceFile(localPath, data); err != nil {
		saveRuleSourceError(dbPath, scope, source.ID, err)
		return nil, err
	}
	result := &RuleSourceSyncResult{
		Data:        data,
		LocalPath:   localPath,
		RuleCount:   count,
		LastUpdated: time.Now(),
	}
	saveRuleSourceSuccess(dbPath, scope, source.ID, result.RuleCount, result.LastUpdated)
	return result, nil
}

func loadExistingRuleSource(
	dbPath string,
	scope rulesource.Scope,
	source rulesource.Source,
	localPath string,
) (*RuleSourceSyncResult, error) {
	data, err := os.ReadFile(localPath)
	if err != nil {
		saveRuleSourceError(dbPath, scope, source.ID, err)
		return nil, err
	}
	info, err := os.Stat(localPath)
	if err != nil {
		saveRuleSourceError(dbPath, scope, source.ID, err)
		return nil, err
	}
	count, err := parseRuleSourceCount(source, data)
	if err != nil {
		saveRuleSourceError(dbPath, scope, source.ID, err)
		return nil, err
	}
	statuses, _ := ListRuleSourceStatusByScope(dbPath, scope)
	lastUpdated := info.ModTime()
	if status, ok := statuses[source.ID]; ok && !status.LastUpdated.IsZero() {
		lastUpdated = status.LastUpdated
	}
	saveRuleSourceSuccess(dbPath, scope, source.ID, count, lastUpdated)
	return &RuleSourceSyncResult{
		Data:        data,
		LocalPath:   localPath,
		RuleCount:   count,
		LastUpdated: lastUpdated,
	}, nil
}

func parseRuleSourceCount(source rulesource.Source, data []byte) (int, error) {
	switch source.Behavior {
	case rulesource.BehaviorAdguard:
		result, err := rulesource.ParseAdguardBytes(source.Format, data)
		return result.Count(), err
	case rulesource.BehaviorDomain:
		rules, err := rulesource.ParseDomainBytes(source.Format, data)
		return len(rules), err
	case rulesource.BehaviorIPCIDR:
		prefixes, err := rulesource.ParseIPCIDRBytes(source.Format, data)
		return len(prefixes), err
	default:
		return 0, fmt.Errorf("unsupported behavior %q", source.Behavior)
	}
}

func writeRuleSourceFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return writeTextFileAtomically(path, data)
}

func saveRuleSourceError(dbPath string, scope rulesource.Scope, sourceID string, err error) {
	_ = SaveRuleSourceStatus(dbPath, RuleSourceStatus{
		Scope:     string(scope),
		SourceID:  sourceID,
		LastError: err.Error(),
	})
}

func saveRuleSourceSuccess(
	dbPath string,
	scope rulesource.Scope,
	sourceID string,
	ruleCount int,
	lastUpdated time.Time,
) {
	_ = SaveRuleSourceStatus(dbPath, RuleSourceStatus{
		Scope:       string(scope),
		SourceID:    sourceID,
		RuleCount:   ruleCount,
		LastUpdated: lastUpdated,
	})
}
