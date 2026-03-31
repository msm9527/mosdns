package coremain

import "github.com/IrineSistiana/mosdns/v5/mlog"

func resolveLogConfigForBaseDir(baseDir string, cfg mlog.LogConfig) mlog.LogConfig {
	resolved := cfg
	resolved.File = ResolveMainConfigPathForBaseDir(baseDir, cfg.File)
	return resolved
}
