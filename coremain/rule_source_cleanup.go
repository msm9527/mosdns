package coremain

import (
	"fmt"
	"os"
	"strings"

	"github.com/IrineSistiana/mosdns/v5/pkg/rulesource"
)

const (
	ruleSourceFileCleanupDeleted = "deleted"
	ruleSourceFileCleanupMissing = "missing"
	ruleSourceFileCleanupSkipped = "skipped"
	ruleSourceFileCleanupError   = "error"
)

type RuleSourceFileCleanup struct {
	Path    string `json:"path,omitempty"`
	Status  string `json:"status"`
	Deleted bool   `json:"deleted"`
	Message string `json:"message,omitempty"`
}

type RuleSourceDeleteResponse struct {
	Message     string                `json:"message"`
	ID          string                `json:"id"`
	Scope       string                `json:"scope"`
	FileCleanup RuleSourceFileCleanup `json:"file_cleanup"`
}

func (s *ruleSourceService) cleanupRemovedSourceFile(
	source rulesource.Source,
	remaining rulesource.Config,
) RuleSourceFileCleanup {
	return s.cleanupRuleSourceFile(source, remaining)
}

func (s *ruleSourceService) cleanupUpdatedSourceFile(
	previous rulesource.Source,
	current rulesource.Source,
	remaining rulesource.Config,
) RuleSourceFileCleanup {
	oldPath, err := normalizedRuleSourcePath(s.scope, previous)
	if err != nil {
		return RuleSourceFileCleanup{
			Status:  ruleSourceFileCleanupError,
			Message: fmt.Sprintf("normalize previous source path: %v", err),
		}
	}
	newPath, err := normalizedRuleSourcePath(s.scope, current)
	if err != nil {
		return RuleSourceFileCleanup{
			Path:    oldPath,
			Status:  ruleSourceFileCleanupError,
			Message: fmt.Sprintf("normalize current source path: %v", err),
		}
	}
	if oldPath == newPath {
		return RuleSourceFileCleanup{
			Path:    oldPath,
			Status:  ruleSourceFileCleanupSkipped,
			Message: "旧文件路径未变化，无需清理",
		}
	}
	return s.cleanupRuleSourceFile(previous, remaining)
}

func (s *ruleSourceService) cleanupRuleSourceFile(
	source rulesource.Source,
	remaining rulesource.Config,
) RuleSourceFileCleanup {
	path, err := normalizedRuleSourcePath(s.scope, source)
	if err != nil {
		return RuleSourceFileCleanup{
			Status:  ruleSourceFileCleanupError,
			Message: fmt.Sprintf("normalize source path: %v", err),
		}
	}
	if !isManagedRuleSourcePath(s.scope, path) {
		return RuleSourceFileCleanup{
			Path:    path,
			Status:  ruleSourceFileCleanupSkipped,
			Message: "路径不在受管规则目录下，已保留原文件",
		}
	}
	if isRuleSourcePathStillReferenced(remaining.Sources, s.scope, path) {
		return RuleSourceFileCleanup{
			Path:    path,
			Status:  ruleSourceFileCleanupSkipped,
			Message: "路径仍被其他规则源引用，已保留原文件",
		}
	}

	localPath, err := rulesource.ResolveLocalPath(s.baseDir(), s.scope, source)
	if err != nil {
		return RuleSourceFileCleanup{
			Path:    path,
			Status:  ruleSourceFileCleanupError,
			Message: fmt.Sprintf("resolve local path: %v", err),
		}
	}
	if err := os.Remove(localPath); err != nil {
		if os.IsNotExist(err) {
			return RuleSourceFileCleanup{
				Path:    path,
				Status:  ruleSourceFileCleanupMissing,
				Message: "本地规则文件不存在，无需删除",
			}
		}
		return RuleSourceFileCleanup{
			Path:    path,
			Status:  ruleSourceFileCleanupError,
			Message: fmt.Sprintf("remove local file: %v", err),
		}
	}
	return RuleSourceFileCleanup{
		Path:    path,
		Status:  ruleSourceFileCleanupDeleted,
		Deleted: true,
		Message: "已删除本地规则文件",
	}
}

func normalizedRuleSourcePath(scope rulesource.Scope, source rulesource.Source) (string, error) {
	return rulesource.NormalizeRelativePath(effectiveSourcePath(scope, source))
}

func isManagedRuleSourcePath(scope rulesource.Scope, path string) bool {
	dir := string(scope)
	return path == dir || strings.HasPrefix(path, dir+"/")
}

func isRuleSourcePathStillReferenced(
	sources []rulesource.Source,
	scope rulesource.Scope,
	target string,
) bool {
	for _, source := range sources {
		path, err := normalizedRuleSourcePath(scope, source)
		if err != nil {
			continue
		}
		if path == target {
			return true
		}
	}
	return false
}
