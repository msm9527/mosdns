package coremain

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/IrineSistiana/mosdns/v5/mlog"
	"github.com/IrineSistiana/mosdns/v5/pkg/rulesource"
	"go.uber.org/zap"
)

type RuleSourceSyncResult struct {
	Data        []byte
	LocalPath   string
	RuleCount   int
	LastUpdated time.Time
	FileSize    int64
	FileModTime time.Time
}

type RuleSourceSyncOptions struct {
	ForceRemote  bool
	PreferCache  bool
	MetadataOnly bool
}

type RuleSourceVersion struct {
	SourceID          string
	LocalPath         string
	FileSize          int64
	FileModTimeUnixNS int64
}

func NewRuleSourceVersion(sourceID string, result *RuleSourceSyncResult) RuleSourceVersion {
	if result == nil {
		return RuleSourceVersion{SourceID: sourceID}
	}
	return RuleSourceVersion{
		SourceID:          sourceID,
		LocalPath:         result.LocalPath,
		FileSize:          result.FileSize,
		FileModTimeUnixNS: result.FileModTime.UnixNano(),
	}
}

func RuleSourceVersionsEqual(a, b []RuleSourceVersion) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func SyncRuleSource(
	ctx context.Context,
	client *http.Client,
	dbPath string,
	baseDir string,
	scope rulesource.Scope,
	source rulesource.Source,
	options RuleSourceSyncOptions,
) (*RuleSourceSyncResult, error) {
	localPath, err := rulesource.ResolveLocalPath(baseDir, scope, source)
	if err != nil {
		return nil, err
	}
	if source.SourceKind == rulesource.SourceKindRemote {
		if options.PreferCache && !options.ForceRemote && localRuleSourceExists(localPath) {
			if options.MetadataOnly {
				return inspectExistingRuleSource(dbPath, scope, source, localPath)
			}
			return loadExistingRuleSource(dbPath, scope, source, localPath)
		}
		if shouldDownloadRuleSource(dbPath, scope, source, localPath, options) {
			return downloadRuleSource(ctx, client, dbPath, scope, source, localPath)
		}
	}
	if options.MetadataOnly {
		return inspectExistingRuleSource(dbPath, scope, source, localPath)
	}
	return loadExistingRuleSource(dbPath, scope, source, localPath)
}

func localRuleSourceExists(localPath string) bool {
	_, err := os.Stat(localPath)
	return err == nil
}

func shouldDownloadRuleSource(
	dbPath string,
	scope rulesource.Scope,
	source rulesource.Source,
	localPath string,
	options RuleSourceSyncOptions,
) bool {
	if options.ForceRemote {
		return true
	}
	if !localRuleSourceExists(localPath) {
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
		return fallbackToExistingRuleSource(dbPath, scope, source, localPath, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		err := fmt.Errorf("download %s failed with status %d", source.ID, resp.StatusCode)
		return fallbackToExistingRuleSource(dbPath, scope, source, localPath, err)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return fallbackToExistingRuleSource(dbPath, scope, source, localPath, err)
	}
	count, err := parseRuleSourceCount(source, data)
	if err != nil {
		return fallbackToExistingRuleSource(dbPath, scope, source, localPath, err)
	}
	if err := writeRuleSourceFile(localPath, data); err != nil {
		return fallbackToExistingRuleSource(dbPath, scope, source, localPath, err)
	}
	result := &RuleSourceSyncResult{
		Data:        data,
		LocalPath:   localPath,
		RuleCount:   count,
		LastUpdated: time.Now(),
		FileSize:    int64(len(data)),
	}
	if info, err := os.Stat(localPath); err == nil {
		result.FileModTime = info.ModTime()
	} else {
		result.FileModTime = result.LastUpdated
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
	result, err := readExistingRuleSource(dbPath, scope, source, localPath)
	if err != nil {
		saveRuleSourceError(dbPath, scope, source.ID, err)
		return nil, err
	}
	saveRuleSourceSuccess(dbPath, scope, source.ID, result.RuleCount, result.LastUpdated)
	return result, nil
}

func inspectExistingRuleSource(
	dbPath string,
	scope rulesource.Scope,
	source rulesource.Source,
	localPath string,
) (*RuleSourceSyncResult, error) {
	info, err := os.Stat(localPath)
	if err != nil {
		return nil, err
	}
	statuses, _ := ListRuleSourceStatusByScope(dbPath, scope)
	lastUpdated := info.ModTime()
	ruleCount := 0
	if status, ok := statuses[source.ID]; ok {
		ruleCount = status.RuleCount
		if !status.LastUpdated.IsZero() {
			lastUpdated = status.LastUpdated
		}
	}
	return &RuleSourceSyncResult{
		LocalPath:   localPath,
		RuleCount:   ruleCount,
		LastUpdated: lastUpdated,
		FileSize:    info.Size(),
		FileModTime: info.ModTime(),
	}, nil
}

func fallbackToExistingRuleSource(
	dbPath string,
	scope rulesource.Scope,
	source rulesource.Source,
	localPath string,
	cause error,
) (*RuleSourceSyncResult, error) {
	result, err := readExistingRuleSource(dbPath, scope, source, localPath)
	if err != nil {
		combinedErr := fmt.Errorf(
			"refresh %s from %s failed: %w; fallback cache %s unavailable: %v",
			source.ID,
			source.URL,
			cause,
			localPath,
			err,
		)
		saveRuleSourceError(dbPath, scope, source.ID, combinedErr)
		return nil, combinedErr
	}
	if saveErr := SaveRuleSourceStatus(dbPath, RuleSourceStatus{
		Scope:       string(scope),
		SourceID:    source.ID,
		RuleCount:   result.RuleCount,
		LastUpdated: result.LastUpdated,
		LastError:   cause.Error(),
	}); saveErr != nil {
		mlog.L().Warn("failed to save rule source status during fallback",
			zap.String("source", source.ID), zap.Error(saveErr))
	}
	return result, nil
}

func readExistingRuleSource(
	dbPath string,
	scope rulesource.Scope,
	source rulesource.Source,
	localPath string,
) (*RuleSourceSyncResult, error) {
	data, err := os.ReadFile(localPath)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(localPath)
	if err != nil {
		return nil, err
	}
	count, err := parseRuleSourceCount(source, data)
	if err != nil {
		return nil, err
	}
	statuses, _ := ListRuleSourceStatusByScope(dbPath, scope)
	lastUpdated := info.ModTime()
	if status, ok := statuses[source.ID]; ok && !status.LastUpdated.IsZero() {
		lastUpdated = status.LastUpdated
	}
	return &RuleSourceSyncResult{
		Data:        data,
		LocalPath:   localPath,
		RuleCount:   count,
		LastUpdated: lastUpdated,
		FileSize:    info.Size(),
		FileModTime: info.ModTime(),
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
	if saveErr := SaveRuleSourceStatus(dbPath, RuleSourceStatus{
		Scope:     string(scope),
		SourceID:  sourceID,
		LastError: err.Error(),
	}); saveErr != nil {
		mlog.L().Warn("failed to save rule source error status",
			zap.String("source", sourceID), zap.Error(saveErr))
	}
}

func saveRuleSourceSuccess(
	dbPath string,
	scope rulesource.Scope,
	sourceID string,
	ruleCount int,
	lastUpdated time.Time,
) {
	if saveErr := SaveRuleSourceStatus(dbPath, RuleSourceStatus{
		Scope:       string(scope),
		SourceID:    sourceID,
		RuleCount:   ruleCount,
		LastUpdated: lastUpdated,
	}); saveErr != nil {
		mlog.L().Warn("failed to save rule source success status",
			zap.String("source", sourceID), zap.Error(saveErr))
	}
}
