package coremain

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/IrineSistiana/mosdns/v5/mlog"
	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"
	"golang.org/x/net/proxy"
)

const (
	maxConfigDownloadBytes int64  = 100 << 20 // 100 MiB
	maxConfigZipEntries           = 2048
	maxConfigZipEntryBytes uint64 = 20 << 20 // 20 MiB
	maxConfigZipTotalBytes uint64 = 200 << 20
	configBackupDirName           = "backup"
)

// ConfigManagerRequest 定义前端传入的参数
type ConfigManagerRequest struct {
	URL string `json:"url"` // 在线更新用的下载地址
	Dir string `json:"dir"` // 本地配置所在的目录
}

// RegisterConfigManagerAPI 注册配置管理相关的 API
func RegisterConfigManagerAPI(router *chi.Mux) {
	router.Post("/api/v1/config/export", handleConfigExport)
	router.Post("/api/v1/config/update_from_url", handleConfigUpdateFromURL)
}

// handleConfigExport 对应需求：把本地目录打包下载
func handleConfigExport(w http.ResponseWriter, r *http.Request) {
	var req ConfigManagerRequest
	if err := decodeJSONBodyStrict(w, r, &req, false); err != nil {
		if errors.Is(err, errJSONBodyTooLarge) {
			writeAPIError(w, http.StatusRequestEntityTooLarge, "REQUEST_BODY_TOO_LARGE", "Request body too large")
			return
		}
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST_BODY", "Invalid request body")
		return
	}
	validatedDir, err := validateConfigTargetDir(req.Dir)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_TARGET_DIR", "invalid dir: "+err.Error())
		return
	}
	req.Dir = validatedDir

	// 设置响应头，告诉浏览器这是一个附件下载
	w.Header().Set("Content-Type", "application/zip")
	filename := fmt.Sprintf("mosdns_backup_%d.zip", time.Now().Unix())
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filename))

	zipWriter := zip.NewWriter(w)
	defer zipWriter.Close()

	rootBackupDir := filepath.Clean(filepath.Join(req.Dir, configBackupDirName))

	// 遍历目录并打包
	err = filepath.Walk(req.Dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// 仅排除根目录下的 backup 文件夹，避免递归备份或下载无用数据。
		// 子目录中的同名 backup 不应被误跳过。
		if info.IsDir() && filepath.Clean(path) == rootBackupDir {
			return filepath.SkipDir
		}
		if info.IsDir() {
			return nil
		}

		// 获取相对于根目录的路径，作为 zip 内的文件名
		relPath, err := filepath.Rel(req.Dir, path)
		if err != nil {
			return err
		}

		// 写入 Zip
		zipFile, err := zipWriter.Create(relPath)
		if err != nil {
			return err
		}

		fsFile, err := os.Open(path)
		if err != nil {
			return err
		}
		defer fsFile.Close()

		_, err = io.Copy(zipFile, fsFile)
		return err
	})

	if err != nil {
		mlog.L().Error("export config failed", zap.String("dir", req.Dir), zap.Error(err))
	}
}

// handleConfigUpdateFromURL 对应需求：下载 -> 备份 -> 覆盖 -> 重启
func handleConfigUpdateFromURL(w http.ResponseWriter, r *http.Request) {
	var req ConfigManagerRequest
	if err := decodeJSONBodyStrict(w, r, &req, false); err != nil {
		if errors.Is(err, errJSONBodyTooLarge) {
			writeAPIError(w, http.StatusRequestEntityTooLarge, "REQUEST_BODY_TOO_LARGE", "Request body too large")
			return
		}
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST_BODY", "Invalid request body")
		return
	}
	if req.URL == "" || req.Dir == "" {
		writeAPIError(w, http.StatusBadRequest, "URL_AND_DIR_REQUIRED", "url and dir are required")
		return
	}
	validatedDir, err := validateConfigTargetDir(req.Dir)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_TARGET_DIR", "invalid dir: "+err.Error())
		return
	}
	req.Dir = validatedDir
	if err := validateConfigUpdateURL(req.URL); err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_UPDATE_URL", "invalid url: "+err.Error())
		return
	}

	lg := mlog.L()

	// --- 1. 下载文件 (包含代理检测和降级逻辑) ---
	zipData, err := downloadWithFallback(req.URL)
	if err != nil {
		lg.Error("download config failed", zap.Error(err))
		writeAPIError(w, http.StatusInternalServerError, "DOWNLOAD_CONFIG_FAILED", "Download failed: "+err.Error())
		return
	}

	// --- 2. 执行备份 (先清空后备份，失败则熔断) ---
	backupDir := filepath.Join(req.Dir, configBackupDirName)
	if err := performLocalBackup(req.Dir, backupDir); err != nil {
		lg.Error("local backup failed, aborting update", zap.Error(err))
		writeAPIError(w, http.StatusInternalServerError, "LOCAL_BACKUP_FAILED", "Backup failed (update aborted): "+err.Error())
		return
	}

	// --- 3. 解压并覆盖（先 staging 再原子写入；失败自动回滚） ---
	updatedCount, err := extractAndOverwriteWithRollback(zipData, req.Dir, backupDir)
	if err != nil {
		lg.Error("extract and overwrite failed", zap.Error(err))
		writeAPIError(w, http.StatusInternalServerError, "UPDATE_FILES_FAILED", "Update files failed: "+err.Error())
		return
	}

	// --- 4. 成功响应并触发重启 ---
	lg.Info("config update successful", zap.Int("files_updated", updatedCount))

	writeJSON(w, http.StatusOK, map[string]any{
		"message": fmt.Sprintf("Update successful. %d files updated. Restarting...", updatedCount),
		"status":  "success",
	})

	// 异步触发重启，给前端一点时间处理响应
	go func() {
		time.Sleep(500 * time.Millisecond)
		triggerRestart()
	}()
}

// downloadWithFallback 尝试使用配置的 Socks5 下载，失败则直连
func downloadWithFallback(url string) ([]byte, error) {
	// 1. 尝试获取代理配置
	var proxyAddr string
	overridesPath := filepath.Join(MainConfigBaseDir, overridesFilename)
	data, err := os.ReadFile(overridesPath)
	if err == nil {
		var temp struct {
			Socks5 string `json:"socks5"`
		}
		if json.Unmarshal(data, &temp) == nil {
			proxyAddr = temp.Socks5
		}
	}

	// 2. 如果有代理，先尝试代理下载
	if proxyAddr != "" {
		mlog.L().Info("attempting download via proxy", zap.String("proxy", proxyAddr))
		data, err := doDownload(url, proxyAddr)
		if err == nil {
			return data, nil
		}
		mlog.L().Warn("download via proxy failed, falling back to direct", zap.Error(err))
	}

	// 3. 直连下载 (Fallback)
	mlog.L().Info("attempting direct download")
	return doDownload(url, "")
}

func doDownload(url, proxyAddr string) ([]byte, error) {
	return doDownloadWithLimit(url, proxyAddr, maxConfigDownloadBytes)
}

func doDownloadWithLimit(url, proxyAddr string, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		return nil, fmt.Errorf("invalid max download size")
	}

	client := &http.Client{Timeout: 60 * time.Second}

	if proxyAddr != "" {
		dialer, err := proxy.SOCKS5("tcp", proxyAddr, nil, proxy.Direct)
		if err != nil {
			return nil, err
		}
		contextDialer, ok := dialer.(proxy.ContextDialer)
		if !ok {
			return nil, fmt.Errorf("proxy dialer does not support context")
		}
		client.Transport = &http.Transport{
			DialContext: contextDialer.DialContext,
		}
	}

	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http status %s", resp.Status)
	}
	if resp.ContentLength > maxBytes {
		return nil, fmt.Errorf("download too large: %d bytes > %d bytes", resp.ContentLength, maxBytes)
	}

	bodyReader := io.LimitReader(resp.Body, maxBytes+1)
	body, err := io.ReadAll(bodyReader)
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxBytes {
		return nil, fmt.Errorf("download too large: exceeds %d bytes", maxBytes)
	}
	return body, nil
}

// performLocalBackup 将 source 目录备份到 dest，备份前先清空 dest
func performLocalBackup(source, dest string) error {
	// 1. 清空旧备份
	if err := os.RemoveAll(dest); err != nil {
		return fmt.Errorf("clean backup dir failed: %w", err)
	}
	// 2. 创建新备份目录
	if err := os.MkdirAll(dest, 0755); err != nil {
		return fmt.Errorf("create backup dir failed: %w", err)
	}

	// 3. 递归复制
	return filepath.Walk(source, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// 跳过备份目录本身
		if path == dest || strings.HasPrefix(path, dest+string(os.PathSeparator)) {
			return nil
		}

		// 计算相对路径
		relPath, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		targetPath := filepath.Join(dest, relPath)

		if info.IsDir() {
			return os.MkdirAll(targetPath, info.Mode())
		}
		if info.Mode()&os.ModeSymlink != 0 {
			// Skip symlink to avoid unexpected traversal behavior in backup artifacts.
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
			return err
		}

		// 复制文件内容
		srcFile, err := os.Open(path)
		if err != nil {
			return err
		}
		defer srcFile.Close()

		dstFile, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
		if err != nil {
			return err
		}
		defer dstFile.Close()

		_, err = io.Copy(dstFile, srcFile)
		if err != nil {
			return err
		}
		return dstFile.Chmod(info.Mode().Perm())
	})
}

// extractAndOverwrite 解压 ZIP 并覆盖本地文件
func extractAndOverwrite(zipData []byte, targetDir string) (int, error) {
	return extractAndOverwriteWithLimits(
		zipData,
		targetDir,
		maxConfigZipEntries,
		maxConfigZipEntryBytes,
		maxConfigZipTotalBytes,
	)
}

func extractAndOverwriteWithRollback(zipData []byte, targetDir, backupDir string) (int, error) {
	stageDir, err := os.MkdirTemp("", "mosdns-update-stage-*")
	if err != nil {
		return 0, fmt.Errorf("create staging dir failed: %w", err)
	}
	defer os.RemoveAll(stageDir)

	if _, err := extractAndOverwriteWithLimits(
		zipData,
		stageDir,
		maxConfigZipEntries,
		maxConfigZipEntryBytes,
		maxConfigZipTotalBytes,
	); err != nil {
		return 0, err
	}

	return applyStagedFilesWithRollback(stageDir, targetDir, backupDir)
}

func applyStagedFilesWithRollback(stageDir, targetDir, backupDir string) (int, error) {
	relFiles, err := listRegularFilesRelative(stageDir)
	if err != nil {
		return 0, err
	}

	applied := make([]string, 0, len(relFiles))
	for _, rel := range relFiles {
		if isInsideBackupDir(rel) {
			return len(applied), fmt.Errorf("update package contains reserved path: %s", rel)
		}

		src := filepath.Join(stageDir, rel)
		dst := filepath.Join(targetDir, rel)
		if err := atomicCopyFile(src, dst); err != nil {
			rollbackErr := rollbackAppliedFiles(applied, backupDir, targetDir)
			if rollbackErr != nil {
				return len(applied), fmt.Errorf("apply failed at %s: %w; rollback failed: %v", rel, err, rollbackErr)
			}
			return len(applied), fmt.Errorf("apply failed at %s: %w; rollback completed", rel, err)
		}
		applied = append(applied, rel)
	}
	return len(applied), nil
}

func rollbackAppliedFiles(applied []string, backupDir, targetDir string) error {
	var rollbackErrs []error

	for _, rel := range applied {
		backupPath := filepath.Join(backupDir, rel)
		targetPath := filepath.Join(targetDir, rel)

		info, err := os.Stat(backupPath)
		switch {
		case err == nil:
			if !info.Mode().IsRegular() {
				rollbackErrs = append(rollbackErrs, fmt.Errorf("rollback source is not regular file: %s", rel))
				continue
			}
			if err := atomicCopyFile(backupPath, targetPath); err != nil {
				rollbackErrs = append(rollbackErrs, fmt.Errorf("rollback restore failed for %s: %w", rel, err))
			}
		case errors.Is(err, os.ErrNotExist):
			if err := os.Remove(targetPath); err != nil && !errors.Is(err, os.ErrNotExist) {
				rollbackErrs = append(rollbackErrs, fmt.Errorf("rollback remove failed for %s: %w", rel, err))
			}
		default:
			rollbackErrs = append(rollbackErrs, fmt.Errorf("rollback stat failed for %s: %w", rel, err))
		}
	}

	return errors.Join(rollbackErrs...)
}

func atomicCopyFile(src, dst string) (err error) {
	srcInfo, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !srcInfo.Mode().IsRegular() {
		return fmt.Errorf("source is not regular file")
	}

	dstDir := filepath.Dir(dst)
	if err := os.MkdirAll(dstDir, 0755); err != nil {
		return err
	}

	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	tmpFile, err := os.CreateTemp(dstDir, ".mosdns-update-*")
	if err != nil {
		return err
	}
	defer func() {
		_ = tmpFile.Close()
		if err != nil {
			_ = os.Remove(tmpFile.Name())
		}
	}()

	if _, err = io.Copy(tmpFile, srcFile); err != nil {
		return err
	}
	if err = tmpFile.Chmod(srcInfo.Mode().Perm()); err != nil {
		return err
	}
	if err = tmpFile.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmpFile.Name(), dst); err != nil {
		return err
	}
	return nil
}

func listRegularFilesRelative(root string) ([]string, error) {
	var files []string

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported file type in update package: %s", path)
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, rel)
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Strings(files)
	return files, nil
}

func isInsideBackupDir(relPath string) bool {
	cleanRel := filepath.Clean(relPath)
	return cleanRel == configBackupDirName || strings.HasPrefix(cleanRel, configBackupDirName+string(os.PathSeparator))
}

func extractAndOverwriteWithLimits(
	zipData []byte,
	targetDir string,
	maxEntries int,
	maxEntryBytes uint64,
	maxTotalBytes uint64,
) (int, error) {
	zipReader, err := zip.NewReader(bytes.NewReader(zipData), int64(len(zipData)))
	if err != nil {
		return 0, fmt.Errorf("invalid zip data: %w", err)
	}
	if len(zipReader.File) > maxEntries {
		return 0, fmt.Errorf("zip has too many entries: %d > %d", len(zipReader.File), maxEntries)
	}

	count := 0
	totalBytes := uint64(0)
	cleanTargetDir := filepath.Clean(targetDir)
	absTargetDir, err := filepath.Abs(cleanTargetDir)
	if err != nil {
		return 0, fmt.Errorf("resolve target dir failed: %w", err)
	}
	for _, f := range zipReader.File {
		if f.FileInfo().IsDir() {
			continue
		}
		if !f.FileInfo().Mode().IsRegular() {
			return count, fmt.Errorf("unsupported zip entry type: %s", f.Name)
		}
		if f.UncompressedSize64 > maxEntryBytes {
			return count, fmt.Errorf("zip entry too large: %s", f.Name)
		}
		totalBytes += f.UncompressedSize64
		if totalBytes > maxTotalBytes {
			return count, fmt.Errorf("zip total uncompressed size too large: %d > %d", totalBytes, maxTotalBytes)
		}

		cleanEntryName := filepath.Clean(f.Name)
		if cleanEntryName == "." || cleanEntryName == ".." || strings.HasPrefix(cleanEntryName, ".."+string(os.PathSeparator)) {
			return count, fmt.Errorf("invalid zip entry path: %s", f.Name)
		}
		if filepath.IsAbs(cleanEntryName) {
			return count, fmt.Errorf("absolute zip entry path is not allowed: %s", f.Name)
		}

		// 构造绝对路径
		fullPath := filepath.Join(cleanTargetDir, cleanEntryName)
		absFullPath, err := filepath.Abs(fullPath)
		if err != nil {
			return count, fmt.Errorf("resolve target file path failed: %s: %w", f.Name, err)
		}

		// 安全检查：防止 zip slip (../../)
		if absFullPath != absTargetDir && !strings.HasPrefix(absFullPath, absTargetDir+string(os.PathSeparator)) {
			return count, fmt.Errorf("zip entry escapes target dir: %s", f.Name)
		}

		// 确保父目录存在
		if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
			return count, fmt.Errorf("create dir failed for %s: %w", f.Name, err)
		}

		// 写入文件 (覆盖模式)
		rc, err := f.Open()
		if err != nil {
			return count, err
		}

		dst, err := os.Create(fullPath)
		if err != nil {
			rc.Close()
			return count, fmt.Errorf("create file %s failed: %w", f.Name, err)
		}

		written, err := io.Copy(dst, io.LimitReader(rc, int64(maxEntryBytes)+1))
		dst.Close()
		rc.Close()

		if err != nil {
			return count, fmt.Errorf("write file %s failed: %w", f.Name, err)
		}
		if uint64(written) > maxEntryBytes {
			return count, fmt.Errorf("zip entry exceeded max size while writing: %s", f.Name)
		}
		if err := os.Chmod(fullPath, f.Mode().Perm()); err != nil {
			return count, fmt.Errorf("set file mode %s failed: %w", f.Name, err)
		}
		count++
	}
	return count, nil
}

func validateConfigUpdateURL(rawURL string) error {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return err
	}
	switch u.Scheme {
	case "http", "https":
	default:
		return fmt.Errorf("unsupported scheme %q", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("missing host")
	}
	return nil
}

func validateConfigTargetDir(rawDir string) (string, error) {
	trimmed := strings.TrimSpace(rawDir)
	if trimmed == "" {
		return "", fmt.Errorf("dir is required")
	}
	dir := filepath.Clean(trimmed)
	info, err := os.Stat(dir)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("not a directory")
	}
	return dir, nil
}

// triggerRestart 尝试重启服务，逻辑对齐 update_manager.go
func triggerRestart() {
	lg := mlog.L()

	// 1. 尝试使用 HTTP API 重启 (优先读取环境变量)
	endpoint := ResolveRestartEndpoint(DefaultRestartEndpoint)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client := &http.Client{Timeout: 5 * time.Second}
	if err := RequestSelfRestart(ctx, client, endpoint, 500); err == nil {
		lg.Info("http restart request sent successfully", zap.String("endpoint", endpoint))
		return
	} else {
		lg.Warn("http restart request failed", zap.String("endpoint", endpoint), zap.Error(err))
	}

	// 2. 如果 HTTP 重启失败且不是 Windows，尝试直接 Exec 重启 (Fallback)
	if SelfRestartSupported() {
		lg.Info("falling back to syscall.Exec for restart")
		// 等待一小会儿确保 HTTP 响应已发送
		time.Sleep(100 * time.Millisecond)
		if err := ExecSelfRestart(); err != nil {
			lg.Error("syscall.Exec failed", zap.Error(err))
		}
	} else {
		lg.Warn("automatic restart failed, manual restart required")
	}
}

func buildRestartRequest(ctx context.Context, endpoint string) (*http.Request, error) {
	return BuildRestartRequestWithDelay(ctx, endpoint, 500)
}
