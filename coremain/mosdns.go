/*
 * Copyright (C) 2020-2022, IrineSistiana
 *
 * This file is part of mosdns.
 *
 * mosdns is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * mosdns is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program.  If not, see <https://www.gnu.org/licenses/>.
 */

package coremain

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/pprof"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/IrineSistiana/mosdns/v5/mlog"
	"github.com/IrineSistiana/mosdns/v5/pkg/safe_close"
	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"
)

type Mosdns struct {
	logger *zap.Logger // non-nil logger.
	env    RuntimeEnv

	// Plugins
	plugins     map[string]any
	pluginTypes map[string]string

	httpMux          *chi.Mux
	metricsReg       *prometheus.Registry
	sc               *safe_close.SafeClose
	overridesMu      sync.RWMutex
	globalOverrides  *GlobalOverrides // <<< ADDED
	cachePolicies    *CachePolicyConfig
	updateManager    *UpdateManager
	restartScheduled atomic.Bool
}

// NewMosdns initializes a mosdns instance and its plugins.
func NewMosdns(cfg *Config) (*Mosdns, error) {
	// Init logger.
	baseLogger, err := mlog.NewLogger(cfg.Log)
	if err != nil {
		return nil, fmt.Errorf("failed to init logger: %w", err)
	}

	// Create our TeeCore to also write to the in-memory collector for detailed process logs.
	teeCore := NewTeeCore(baseLogger.Core(), GlobalLogCollector)

	// Create the final logger with our TeeCore.
	lg := zap.New(teeCore, zap.AddCaller(), zap.AddStacktrace(zap.ErrorLevel))

	// Start the audit log collector's background worker.
	GlobalAuditCollector.StartWorker()

	env := newRuntimeEnvFromConfig(cfg)
	m := &Mosdns{
		logger:        lg,
		env:           env,
		plugins:       make(map[string]any),
		pluginTypes:   make(map[string]string),
		httpMux:       chi.NewRouter(),
		metricsReg:    newMetricsReg(),
		sc:            safe_close.NewSafeClose(),
		updateManager: NewUpdateManager(),
	}
	m.updateManager.SetRuntimeEnv(env)
	m.updateManager.SetCurrentVersion(GetBuildVersion())
	SetConfiguredRestartEndpointFromHTTPAddr(cfg.API.HTTP)
	unregisterRestartScheduler := registerInternalRestartScheduler(func(delayMs int) error {
		_, err := m.ScheduleSelfRestart(delayMs)
		return err
	})
	m.sc.Attach(func(done func(), closeSignal <-chan struct{}) {
		defer done()
		<-closeSignal
		unregisterRestartScheduler()
		ClearConfiguredRestartEndpoint()
	})

	// <<< START OF MODIFICATIONS >>>
	// Step 1: Discover original settings from the raw config.
	// This maintains the original logic for API fallbacks.
	DiscoverAndCacheSettings(cfg)

	// Step 2: Load user-editable overrides from custom_config.
	if overrides, ok, err := loadGlobalOverridesFromCustomConfigForBaseDir(env.BaseDir); err == nil && ok {
		m.setGlobalOverrides(overrides)
		mlog.L().Info("loaded global overrides from custom config",
			zap.String("path", globalOverridesConfigPathForBaseDir(env.BaseDir)),
			zap.String("socks5", overrides.Socks5),
			zap.String("ecs", overrides.ECS),
			zap.Int("replacements", len(overrides.Replacements)))
	} else if err != nil {
		mlog.L().Warn("failed to load global overrides from custom config", zap.Error(err))
	}
	cachePolicies, ok, err := LoadCachePolicyConfigFromSubConfigForBaseDir(env.BaseDir)
	if err != nil {
		return nil, fmt.Errorf("failed to load cache policies from sub config: %w", err)
	}
	m.cachePolicies = cachePolicies
	if ok {
		mlog.L().Info("loaded cache policies from sub config",
			zap.String("path", cachePoliciesConfigPathForBaseDir(env.BaseDir)),
			zap.Int("response_policies", len(cachePolicies.Response)),
			zap.Int("udp_fast_internal_ttl", cachePolicies.UDPFastPath.InternalTTL),
			zap.Int("udp_fast_stale_retry_seconds", cachePolicies.UDPFastPath.StaleRetry))
	}
	// <<< END OF MODIFICATIONS >>>

	// This must be called after m.httpMux and m.metricsReg been set.
	m.initHttpMux()

	// Register our new APIs.
	RegisterCaptureAPI(m.httpMux)      // For process logs
	RegisterAuditAPI(m.httpMux, m)     // For audit logs
	RegisterOverridesAPI(m.httpMux, m) // <<< MODIFIED: Pass 'm'
	RegisterConfigManagerAPI(m.httpMux, m)
	RegisterCacheAPI(m.httpMux, m)
	RegisterListsAPI(m.httpMux, m)
	RegisterMiscAPI(m.httpMux, m)
	RegisterMemoryAPI(m.httpMux, m)
	RegisterRuntimeAPI(m.httpMux, m)
	RegisterRuntimeStatsAPI(m.httpMux, m)
	RegisterRulesAPI(m.httpMux, m)
	RegisterUpdateAPI(m.httpMux, m) // For binary updates
	RegisterSystemAPI(m.httpMux, m) // For self-restart
	RegisterUpstreamAPI(m.httpMux, m)

	// Start http api server
	if httpAddr := cfg.API.HTTP; len(httpAddr) > 0 {
		httpServer := &http.Server{
			Addr:    httpAddr,
			Handler: m.httpMux,
		}
		m.sc.Attach(func(done func(), closeSignal <-chan struct{}) {
			defer done()
			errChan := make(chan error, 1)
			go func() {
				m.logger.Info("starting api http server", zap.String("addr", httpAddr))
				errChan <- httpServer.ListenAndServe()
			}()
			select {
			case err := <-errChan:
				m.sc.SendCloseSignal(err)
			case <-closeSignal:
				_ = httpServer.Close()
			}
		})
	}

	// Load plugins.

	// Close all plugins on signal.
	m.sc.Attach(func(done func(), closeSignal <-chan struct{}) {
		go func() {
			defer done()
			<-closeSignal

			// Stop the audit worker gracefully.
			GlobalAuditCollector.Stop()

			m.shutdownPlugins()
			GlobalAuditCollector.StopWorker()
		}()
	})

	// Preset plugins
	if err := m.loadPresetPlugins(); err != nil {
		m.sc.SendCloseSignal(err)
		_ = m.sc.WaitClosed()
		return nil, err
	}
	// Plugins from config.
	if err := m.loadPluginsFromCfg(cfg, 0); err != nil {
		m.sc.SendCloseSignal(err)
		_ = m.sc.WaitClosed()
		return nil, err
	}
	m.logger.Info("all plugins are loaded")

	return m, nil
}

// NewTestMosdnsWithPlugins returns a mosdns instance for testing.
func NewTestMosdnsWithPlugins(p map[string]any) *Mosdns {
	return NewTestMosdnsWithPluginsAndEnv(p, RuntimeEnv{})
}

// NewTestMosdnsWithPluginsAndEnv returns a mosdns test instance with an explicit runtime environment.
func NewTestMosdnsWithPluginsAndEnv(p map[string]any, env RuntimeEnv) *Mosdns {
	if env == (RuntimeEnv{}) {
		env = runtimeEnvFromGlobals()
	} else {
		env = completeRuntimeEnv(env)
	}
	return &Mosdns{
		logger:      mlog.Nop(),
		env:         env,
		httpMux:     chi.NewRouter(),
		plugins:     p,
		pluginTypes: make(map[string]string),
		metricsReg:  newMetricsReg(),
		sc:          safe_close.NewSafeClose(),
	}
}

func (m *Mosdns) GetSafeClose() *safe_close.SafeClose {
	return m.sc
}

// CloseWithErr is a shortcut for m.sc.SendCloseSignal
func (m *Mosdns) CloseWithErr(err error) {
	m.sc.SendCloseSignal(err)
}

// Logger returns a non-nil logger.
func (m *Mosdns) Logger() *zap.Logger {
	return m.logger
}

// GetPlugin returns a plugin.
func (m *Mosdns) GetPlugin(tag string) any {
	return m.plugins[tag]
}

// GetMetricsReg returns a prometheus.Registerer with a prefix of "mosdns_"
func (m *Mosdns) GetMetricsReg() prometheus.Registerer {
	return prometheus.WrapRegistererWithPrefix("mosdns_", m.metricsReg)
}

func (m *Mosdns) GetAPIRouter() *chi.Mux {
	return m.httpMux
}

func (m *Mosdns) tryScheduleRestart() bool {
	return m.restartScheduled.CompareAndSwap(false, true)
}

func (m *Mosdns) clearScheduledRestart() {
	m.restartScheduled.Store(false)
}

func (m *Mosdns) setGlobalOverrides(overrides *GlobalOverrides) {
	m.overridesMu.Lock()
	m.globalOverrides = overrides
	m.overridesMu.Unlock()
}

func (m *Mosdns) getGlobalOverridesRef() *GlobalOverrides {
	m.overridesMu.RLock()
	defer m.overridesMu.RUnlock()
	return m.globalOverrides
}

// GetGlobalOverrides returns a deep copy of current runtime global overrides.
func (m *Mosdns) GetGlobalOverrides() *GlobalOverrides {
	m.overridesMu.RLock()
	defer m.overridesMu.RUnlock()
	return CloneGlobalOverrides(m.globalOverrides)
}

func (m *Mosdns) RuntimeEnv() RuntimeEnv {
	if m == nil {
		return runtimeEnvFromGlobals()
	}
	if m.env == (RuntimeEnv{}) {
		return runtimeEnvFromGlobals()
	}
	return m.env
}

func (m *Mosdns) BaseDir() string {
	return m.RuntimeEnv().BaseDir
}

func (m *Mosdns) MainConfigPath() string {
	return m.RuntimeEnv().MainConfigPath
}

func (m *Mosdns) ControlDBPath() string {
	return m.RuntimeEnv().ControlDBPath
}

func (m *Mosdns) GetUpdateManager() *UpdateManager {
	if m == nil || m.updateManager == nil {
		return GlobalUpdateManager
	}
	return m.updateManager
}

func newMetricsReg() *prometheus.Registry {
	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	reg.MustRegister(collectors.NewGoCollector())
	return reg
}

// initHttpMux initializes api entries. It MUST be called after m.metricsReg being initialized.
func (m *Mosdns) initHttpMux() {
	// 全局 CORS 中间件
	corsMiddleware := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			// <<< MODIFIED: Allow PUT and DELETE methods for plugin APIs
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS, PUT, DELETE")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Accept")

			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusOK)
				return
			}
			next.ServeHTTP(w, r)
		})
	}

	m.httpMux.Use(corsMiddleware)

	// metrics 处理 (只注册一次)
	metricsHandler := promhttp.HandlerFor(m.metricsReg, promhttp.HandlerOpts{})
	wrappedMetricsHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.logger.Debug("Metrics endpoint accessed",
			zap.String("remote_addr", r.RemoteAddr),
			zap.String("method", r.Method))
		metricsHandler.ServeHTTP(w, r)
	})
	m.httpMux.Method(http.MethodGet, "/metrics", wrappedMetricsHandler)

	// 外置 UI 模式：不再内置任何前端资源，只挂载配置目录下的 ui 文件。
	uiBaseDir := filepath.Join(m.BaseDir(), "ui")
	m.httpMux.Get("/", func(w http.ResponseWriter, r *http.Request) {
		if info, err := os.Stat(uiBaseDir); err == nil && info.IsDir() {
			http.Redirect(w, r, "/ui/", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("No built-in UI. Please deploy external UI under <config-dir>/ui.\n"))
	})

	// 检查 ui 目录是否存在
	if info, err := os.Stat(uiBaseDir); err == nil && info.IsDir() {
		// 挂载 ui 根目录到 /ui
		m.httpMux.Mount("/ui", http.StripPrefix("/ui", http.FileServer(http.Dir(uiBaseDir))))
		m.logger.Info("mounted external ui root", zap.String("route", "/ui"), zap.String("path", uiBaseDir))

		// 定义保留的路由名称，防止外部文件夹覆盖核心功能
		reservedPaths := map[string]bool{
			"debug": true, "metrics": true, "plugins": true, "api": true, "ui": true, "": true,
		}

		// 读取子目录
		entries, err := os.ReadDir(uiBaseDir)
		if err == nil {
			for _, entry := range entries {
				if entry.IsDir() {
					dirName := entry.Name()

					// 检查是否与保留名称冲突
					if reservedPaths[dirName] {
						m.logger.Warn("skipping external ui directory due to naming conflict with reserved route",
							zap.String("name", dirName))
						continue
					}

					// 挂载目录
					routePath := "/" + dirName
					fsPath := filepath.Join(uiBaseDir, dirName)

					// 使用 http.StripPrefix 确保访问子路径时(如 /v1/index.html)能映射到正确的文件
					m.httpMux.Mount(routePath, http.StripPrefix(routePath, http.FileServer(http.Dir(fsPath))))

					m.logger.Info("auto-mounted external ui version",
						zap.String("route", routePath),
						zap.String("path", fsPath))
				}
			}
		}
	}

	// Register pprof.
	m.httpMux.Route("/debug/pprof", func(r chi.Router) {
		r.Get("/*", pprof.Index)
		r.Get("/cmdline", pprof.Cmdline)
		r.Get("/profile", pprof.Profile)
		r.Get("/symbol", pprof.Symbol)
		r.Get("/trace", pprof.Trace)
	})

	m.httpMux.NotFound(writeAPINotFound)
	m.httpMux.MethodNotAllowed(writeAPINotFound)
}

func (m *Mosdns) loadPresetPlugins() error {
	if m.pluginTypes == nil {
		m.pluginTypes = make(map[string]string)
	}
	for tag, f := range LoadNewPersetPluginFuncs() {
		p, err := f(NewBP(tag, m))
		if err != nil {
			return fmt.Errorf("failed to init preset plugin %s, %w", tag, err)
		}
		m.plugins[tag] = p
		m.pluginTypes[tag] = "preset"
	}
	return nil
}

// loadPluginsFromCfg loads plugins from this config. It follows include first.
func (m *Mosdns) loadPluginsFromCfg(cfg *Config, includeDepth int) error {
	const maxIncludeDepth = 8
	if includeDepth > maxIncludeDepth {
		return errors.New("maximum include depth reached")
	}
	includeDepth++

	// Follow include first.
	for _, includePath := range cfg.Include {
		resolvedPath := includePath
		if len(cfg.baseDir) > 0 && !filepath.IsAbs(includePath) {
			resolvedPath = filepath.Join(cfg.baseDir, includePath)
		}
		subCfg, path, err := loadConfig(resolvedPath)
		if err != nil {
			return fmt.Errorf("failed to read config from %s, %w", includePath, err)
		}
		m.logger.Info("load config", zap.String("file", path))
		if err := m.loadPluginsFromCfg(subCfg, includeDepth); err != nil {
			return fmt.Errorf("failed to load config from %s, %w", includePath, err)
		}
	}

	for i, pc := range cfg.Plugins {
		rawPC := pc
		if overrides := m.getGlobalOverridesRef(); overrides != nil {
			ApplyOverrides(pc.Tag, &pc, overrides)
		}
		if err := ApplyRuntimeCachePolicy(&pc, m.cachePolicies); err != nil {
			return fmt.Errorf("failed to apply runtime cache policy to plugin %s, %w", pc.Tag, err)
		}

		if err := m.newPlugin(rawPC, pc); err != nil {
			return fmt.Errorf("failed to init plugin #%d %s, %w", i, pc.Tag, err)
		}
	}
	return nil
}
