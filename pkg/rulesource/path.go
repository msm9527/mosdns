package rulesource

import (
	"fmt"
	"path/filepath"
	"strings"
)

func NormalizeRelativePath(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", nil
	}
	if filepath.IsAbs(path) {
		return "", fmt.Errorf("path must be relative to config base dir")
	}
	clean := filepath.Clean(strings.TrimSpace(path))
	if clean == "." || clean == "" {
		return "", nil
	}
	if strings.HasPrefix(clean, "..") {
		return "", fmt.Errorf("path must stay inside config base dir")
	}
	return filepath.ToSlash(clean), nil
}

func ResolveLocalPath(baseDir string, scope Scope, src Source) (string, error) {
	path, err := NormalizeRelativePath(src.Path)
	if err != nil {
		return "", err
	}
	if path == "" {
		path = DefaultRelativePath(scope, src)
	}
	if strings.TrimSpace(baseDir) == "" {
		baseDir = "."
	}
	return filepath.Join(baseDir, filepath.FromSlash(path)), nil
}
