package coremain

import (
	"path/filepath"
	"strings"
)

type RuntimeEnv struct {
	BaseDir        string
	MainConfigPath string
	ControlDBPath  string
}

func newRuntimeEnvFromConfig(cfg *Config) RuntimeEnv {
	if cfg == nil {
		return RuntimeEnv{}
	}
	env := RuntimeEnv{
		BaseDir:        strings.TrimSpace(cfg.baseDir),
		MainConfigPath: strings.TrimSpace(cfg.mainConfigPath),
		ControlDBPath:  strings.TrimSpace(cfg.ControlDBPath),
	}
	return completeRuntimeEnv(env)
}

func normalizeRuntimeEnv(env RuntimeEnv) RuntimeEnv {
	env.BaseDir = cleanRuntimePath(env.BaseDir)
	env.MainConfigPath = cleanRuntimePath(env.MainConfigPath)
	env.ControlDBPath = cleanRuntimePath(env.ControlDBPath)
	return env
}

func completeRuntimeEnv(env RuntimeEnv) RuntimeEnv {
	env = normalizeRuntimeEnv(env)
	if env.MainConfigPath == "" && env.BaseDir != "" {
		env.MainConfigPath = filepath.Join(env.BaseDir, defaultMainConfigFilename)
	}
	if env.ControlDBPath == "" && env.BaseDir != "" {
		env.ControlDBPath = filepath.Join(env.BaseDir, runtimeStateDBFilename)
	}
	return normalizeRuntimeEnv(env)
}

func cleanRuntimePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	return filepath.Clean(path)
}

func runtimeEnvFromGlobals() RuntimeEnv {
	return normalizeRuntimeEnv(RuntimeEnv{
		BaseDir:        MainConfigBaseDir,
		MainConfigPath: MainConfigFilePath,
		ControlDBPath:  RuntimeStateDBPath(),
	})
}

func applyLegacyRuntimeEnv(env RuntimeEnv) {
	env = normalizeRuntimeEnv(env)
	MainConfigBaseDir = env.BaseDir
	MainConfigFilePath = env.MainConfigPath
	setRuntimeStateDBPath(env.ControlDBPath)
}

func runtimeBaseDir(m *Mosdns) string {
	if m != nil {
		return m.BaseDir()
	}
	return runtimeEnvFromGlobals().BaseDir
}

func runtimeControlDBPath(m *Mosdns) string {
	if m != nil {
		return m.ControlDBPath()
	}
	return runtimeEnvFromGlobals().ControlDBPath
}
