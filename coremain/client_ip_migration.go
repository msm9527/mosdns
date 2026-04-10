package coremain

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/IrineSistiana/mosdns/v5/mlog"
	"github.com/IrineSistiana/mosdns/v5/plugin/switch/switchmeta"
	"go.uber.org/zap"
)

const (
	legacyClientIPListRelPath    = "rule/client_ip.txt"
	clientIPWhitelistListRelPath = "rule/client_ip_whitelist.txt"
	clientIPBlacklistListRelPath = "rule/client_ip_blacklist.txt"
)

func migrateLegacyClientIPListForBaseDir(baseDir string) error {
	legacyPath := ResolveMainConfigPathForBaseDir(baseDir, legacyClientIPListRelPath)
	legacyRaw, err := os.ReadFile(legacyPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read legacy client ip list %s: %w", legacyPath, err)
	}

	whitelistPath := ResolveMainConfigPathForBaseDir(baseDir, clientIPWhitelistListRelPath)
	blacklistPath := ResolveMainConfigPathForBaseDir(baseDir, clientIPBlacklistListRelPath)

	whitelistHasRules, err := fileHasEffectiveRules(whitelistPath)
	if err != nil {
		return fmt.Errorf("inspect client ip whitelist %s: %w", whitelistPath, err)
	}
	blacklistHasRules, err := fileHasEffectiveRules(blacklistPath)
	if err != nil {
		return fmt.Errorf("inspect client ip blacklist %s: %w", blacklistPath, err)
	}
	if whitelistHasRules || blacklistHasRules {
		mlog.L().Warn("legacy client_ip list exists but new client ip lists already contain rules; skip migration",
			zap.String("legacy_path", legacyPath),
			zap.String("whitelist_path", whitelistPath),
			zap.String("blacklist_path", blacklistPath))
		return nil
	}

	switchValues, _, err := loadSwitchesFromCustomConfigForBaseDir(baseDir)
	if err != nil {
		return fmt.Errorf("load switches for client ip migration: %w", err)
	}
	mode := switchValues["client_proxy_mode"]
	if mode == "" {
		mode = switchmeta.MustLookup("client_proxy_mode").DefaultValue
	}

	normalizedLegacy := normalizeRuleFileContent(legacyRaw)
	targets := clientIPMigrationTargets(baseDir, mode)
	for _, path := range targets {
		if err := writeTextFileAtomically(path, normalizedLegacy); err != nil {
			return fmt.Errorf("write migrated client ip list %s: %w", path, err)
		}
	}

	for _, path := range []string{whitelistPath, blacklistPath} {
		if containsString(targets, path) {
			continue
		}
		if err := ensureEmptyTextFile(path); err != nil {
			return fmt.Errorf("ensure empty client ip list %s: %w", path, err)
		}
	}

	if err := os.Remove(legacyPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove legacy client ip list %s: %w", legacyPath, err)
	}

	mlog.L().Info("migrated legacy client_ip list to split allow/deny files",
		zap.String("mode", mode),
		zap.String("legacy_path", legacyPath),
		zap.Strings("targets", relPathsForLog(baseDir, targets)))
	return nil
}

func clientIPMigrationTargets(baseDir, mode string) []string {
	whitelistPath := ResolveMainConfigPathForBaseDir(baseDir, clientIPWhitelistListRelPath)
	blacklistPath := ResolveMainConfigPathForBaseDir(baseDir, clientIPBlacklistListRelPath)

	switch mode {
	case "whitelist":
		return []string{whitelistPath}
	case "blacklist":
		return []string{blacklistPath}
	default:
		return []string{whitelistPath, blacklistPath}
	}
}

func fileHasEffectiveRules(path string) (bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return len(effectiveRuleLines(raw)) > 0, nil
}

func ensureEmptyTextFile(path string) error {
	return writeTextFileAtomically(path, []byte{})
}

func normalizeRuleFileContent(raw []byte) []byte {
	lines := effectiveRuleLines(raw)
	if len(lines) == 0 {
		return []byte{}
	}

	var buf bytes.Buffer
	for _, line := range lines {
		buf.Write(line)
		buf.WriteByte('\n')
	}
	return buf.Bytes()
}

func effectiveRuleLines(raw []byte) [][]byte {
	parts := bytes.Split(raw, []byte{'\n'})
	lines := make([][]byte, 0, len(parts))
	for _, part := range parts {
		trimmed := bytes.TrimSpace(part)
		if len(trimmed) == 0 || trimmed[0] == '#' {
			continue
		}
		lines = append(lines, append([]byte(nil), trimmed...))
	}
	return lines
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func relPathsForLog(baseDir string, paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		if rel, err := filepath.Rel(baseDir, path); err == nil {
			out = append(out, rel)
			continue
		}
		out = append(out, path)
	}
	return out
}
