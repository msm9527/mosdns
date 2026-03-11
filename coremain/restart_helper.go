package coremain

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"syscall"
)

const DefaultRestartEndpoint = "http://127.0.0.1:9099/api/v1/system/restart"

var ErrSelfRestartNotSupported = errors.New("self-restart is not supported on Windows")

// RestartPreparer is implemented by plugins that need explicit preparation
// before a process restart, such as flushing buffered state to disk.
type RestartPreparer interface {
	PrepareForRestart() error
}

func SelfRestartSupported() bool {
	return runtime.GOOS != "windows"
}

func ResolveRestartEndpoint(defaultEndpoint string) string {
	endpoint := strings.TrimSpace(os.Getenv("MOSDNS_RESTART_ENDPOINT"))
	if endpoint == "" {
		endpoint = defaultEndpoint
	}
	return endpoint
}

func BuildRestartRequestWithDelay(ctx context.Context, endpoint string, delayMs int) (*http.Request, error) {
	if delayMs <= 0 {
		delayMs = 500
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(fmt.Sprintf(`{"delay_ms":%d}`, delayMs)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	host := req.URL.Hostname()
	if host == "localhost" || host == "127.0.0.1" {
		req.Host = req.URL.Host
	}
	return req, nil
}

func RequestSelfRestart(ctx context.Context, client *http.Client, endpoint string, delayMs int) error {
	if client == nil {
		client = &http.Client{}
	}
	req, err := BuildRestartRequestWithDelay(ctx, endpoint, delayMs)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %s", resp.Status)
	}
	return nil
}

func ExecSelfRestart() error {
	if !SelfRestartSupported() {
		return ErrSelfRestartNotSupported
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	args := append([]string{exe}, os.Args[1:]...)
	return syscall.Exec(exe, args, os.Environ())
}
