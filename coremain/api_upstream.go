package coremain

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/IrineSistiana/mosdns/v5/mlog"
	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"
)

const upstreamOverridesFilename = "upstream_overrides.json"
const (
	runtimeStateNamespaceUpstreams = "upstreams"
	runtimeStateKeyUpstreamConfig  = "config"
)

// UpstreamOverrideConfig 定义 UI/API 交互的完整数据结构
type UpstreamOverrideConfig struct {
	Tag      string `json:"tag"`      // 上游名称 (Upstream Name)
	Enabled  bool   `json:"enabled"`  // 是否启用
	Protocol string `json:"protocol"` // UI类型: aliapi, udp, tcp, dot, doh...

	// 通用字段
	Addr                 string `json:"addr,omitempty"`
	DialAddr             string `json:"dial_addr,omitempty"`
	IdleTimeout          int    `json:"idle_timeout,omitempty"`
	UpstreamQueryTimeout int    `json:"upstream_query_timeout,omitempty"`

	// DNS (DoT/DoH/TCP/UDP) 专用
	EnablePipeline     bool   `json:"enable_pipeline,omitempty"`
	EnableHTTP3        bool   `json:"enable_http3,omitempty"`
	InsecureSkipVerify bool   `json:"insecure_skip_verify,omitempty"`
	Socks5             string `json:"socks5,omitempty"`
	SoMark             int    `json:"so_mark,omitempty"`
	BindToDevice       string `json:"bind_to_device,omitempty"`
	Bootstrap          string `json:"bootstrap,omitempty"`
	BootstrapVer       int    `json:"bootstrap_version,omitempty"`

	// AliAPI 专用
	AccountID       string `json:"account_id,omitempty"`
	AccessKeyID     string `json:"access_key_id,omitempty"`
	AccessKeySecret string `json:"access_key_secret,omitempty"`
	ServerAddr      string `json:"server_addr,omitempty"`
	EcsClientIP     string `json:"ecs_client_ip,omitempty"`
	EcsClientMask   uint8  `json:"ecs_client_mask,omitempty"`
}

// GlobalUpstreamOverrides 映射关系: 插件Tag -> 上游配置列表
type GlobalUpstreamOverrides map[string][]UpstreamOverrideConfig

var (
	upstreamOverridesLock sync.RWMutex
	upstreamOverrides     GlobalUpstreamOverrides
)

func getUpstreamOverridesPath() (dir string, path string) {
	dir = MainConfigBaseDir
	if dir == "" {
		dir = "."
	}
	path = filepath.Join(dir, upstreamOverridesFilename)
	return dir, path
}

// RegisterUpstreamAPI 注册路由
func RegisterUpstreamAPI(router *chi.Mux, m *Mosdns) {
	router.Route("/api/v1/upstream", func(r chi.Router) {
		r.Get("/tags", handleGetAliAPITags)
		r.Get("/config", handleGetUpstreamConfig)
		r.Put("/config", func(w http.ResponseWriter, r *http.Request) {
			handleReplaceUpstreamConfigWithMosdns(w, r, m)
		})
		r.Post("/config", func(w http.ResponseWriter, r *http.Request) {
			handleSetUpstreamConfigWithMosdns(w, r, m)
		})
		r.Post("/apply", func(w http.ResponseWriter, r *http.Request) {
			handleApplyUpstreamConfigWithMosdns(w, r, m)
		})
		r.Get("/items", handleGetUpstreamItems)
		r.Post("/items", func(w http.ResponseWriter, r *http.Request) {
			handleCreateUpstreamItemWithMosdns(w, r, m)
		})
		r.Put("/items/{upstreamTag}", func(w http.ResponseWriter, r *http.Request) {
			handleUpdateUpstreamItemWithMosdns(w, r, m)
		})
		r.Delete("/items/{upstreamTag}", func(w http.ResponseWriter, r *http.Request) {
			handleDeleteUpstreamItemWithMosdns(w, r, m)
		})
	})
}

type upstreamConfigReplaceRequest struct {
	Config GlobalUpstreamOverrides `json:"config"`
	Apply  bool                    `json:"apply"`
}

type upstreamApplyRequest struct {
	PluginTag string `json:"plugin_tag"`
}

type upstreamItemMutationRequest struct {
	PluginTag string                 `json:"plugin_tag"`
	Upstream  UpstreamOverrideConfig `json:"upstream"`
	Apply     bool                   `json:"apply"`
}

// GetUpstreamOverrides 供 aliapi 插件初始化调用
func GetUpstreamOverrides(pluginTag string) []UpstreamOverrideConfig {
	if err := ensureUpstreamOverridesLoaded(); err != nil {
		mlog.L().Warn("[Debug UpstreamAPI] ensure load failed", zap.Error(err))
		return nil
	}

	upstreamOverridesLock.RLock()
	defer upstreamOverridesLock.RUnlock()

	entries, ok := upstreamOverrides[pluginTag]
	if !ok || len(entries) == 0 {
		return nil
	}
	copied := make([]UpstreamOverrideConfig, len(entries))
	copy(copied, entries)
	return copied
}

// loadUpstreamOverrides 内部加载函数
func loadUpstreamOverrides() error {
	upstreamOverridesLock.Lock()
	defer upstreamOverridesLock.Unlock()
	return loadUpstreamOverridesLocked()
}

func loadUpstreamOverridesLocked() error {
	dir, path := getUpstreamOverridesPath()
	// 获取绝对路径用于 Debug
	absDir, _ := filepath.Abs(dir)

	mlog.L().Info("[Debug UpstreamAPI] Loading overrides",
		zap.String("MainConfigBaseDir", dir),
		zap.String("AbsoluteDir", absDir),
		zap.String("File", path))

	if cfg, ok, err := loadUpstreamOverridesFromRuntimeStore(); err == nil && ok {
		count := 0
		for _, v := range cfg {
			count += len(v)
		}
		mlog.L().Info("[Debug UpstreamAPI] Loaded success from runtime store", zap.Int("groups", len(cfg)), zap.Int("total_items", count))
		upstreamOverrides = cfg
		return nil
	} else if err != nil {
		mlog.L().Warn("[Debug UpstreamAPI] Runtime store load failed, falling back to file", zap.Error(err))
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			mlog.L().Info("[Debug UpstreamAPI] File not found, creating new map", zap.String("path", path))
			upstreamOverrides = make(GlobalUpstreamOverrides)
			return nil
		}
		mlog.L().Error("[Debug UpstreamAPI] Failed to read file", zap.Error(err))
		return err
	}

	var cfg GlobalUpstreamOverrides
	if err := json.Unmarshal(data, &cfg); err != nil {
		mlog.L().Error("[Debug UpstreamAPI] JSON parse error", zap.Error(err))
		return err
	}

	// Count items for debug
	count := 0
	for _, v := range cfg {
		count += len(v)
	}
	mlog.L().Info("[Debug UpstreamAPI] Loaded success", zap.Int("groups", len(cfg)), zap.Int("total_items", count))

	upstreamOverrides = cfg
	return nil
}

// saveUpstreamOverrides 内部保存函数
func saveUpstreamOverrides() error {
	upstreamOverridesLock.Lock()
	defer upstreamOverridesLock.Unlock()
	return saveUpstreamOverridesLocked()
}

func saveUpstreamOverridesLocked() error {
	dir, path := getUpstreamOverridesPath()

	// 确保配置目录存在
	if dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			mlog.L().Error("[Debug UpstreamAPI] Failed to mkdir", zap.String("dir", dir), zap.Error(err))
			return err
		}
	}

	absPath, _ := filepath.Abs(path)

	data, err := json.MarshalIndent(upstreamOverrides, "", "  ")
	if err != nil {
		mlog.L().Error("[Debug UpstreamAPI] JSON marshal failed", zap.Error(err))
		return err
	}

	if err := saveUpstreamOverridesToRuntimeStore(upstreamOverrides); err != nil {
		mlog.L().Error("[Debug UpstreamAPI] Runtime store save failed", zap.Error(err))
		return err
	}

	mlog.L().Info("[Debug UpstreamAPI] Writing to file",
		zap.String("path", path),
		zap.String("abs_path", absPath),
		zap.Int("bytes", len(data)))

	err = os.WriteFile(path, data, 0644)
	if err != nil {
		mlog.L().Error("[Debug UpstreamAPI] WriteFile FAILED", zap.Error(err))
	} else {
		mlog.L().Info("[Debug UpstreamAPI] WriteFile SUCCESS")
	}
	return err
}

func loadUpstreamOverridesFromRuntimeStore() (GlobalUpstreamOverrides, bool, error) {
	store, err := getRuntimeStateStore()
	if err != nil {
		return nil, false, err
	}
	var cfg GlobalUpstreamOverrides
	ok, err := store.get(runtimeStateNamespaceUpstreams, runtimeStateKeyUpstreamConfig, &cfg)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	if cfg == nil {
		cfg = make(GlobalUpstreamOverrides)
	}
	return cfg, true, nil
}

func saveUpstreamOverridesToRuntimeStore(cfg GlobalUpstreamOverrides) error {
	store, err := getRuntimeStateStore()
	if err != nil {
		return err
	}
	if err := store.put(runtimeStateNamespaceUpstreams, runtimeStateKeyUpstreamConfig, cfg); err != nil {
		return err
	}
	totalItems := 0
	for _, items := range cfg {
		totalItems += len(items)
	}
	_ = RecordSystemEvent("runtime.upstreams", "info", "saved upstream overrides", map[string]any{
		"groups":      len(cfg),
		"total_items": totalItems,
	})
	return nil
}

func ensureUpstreamOverridesLoaded() error {
	upstreamOverridesLock.RLock()
	loaded := upstreamOverrides != nil
	upstreamOverridesLock.RUnlock()
	if loaded {
		return nil
	}

	upstreamOverridesLock.Lock()
	defer upstreamOverridesLock.Unlock()
	if upstreamOverrides != nil {
		return nil
	}
	return loadUpstreamOverridesLocked()
}

func cloneUpstreamList(src []UpstreamOverrideConfig) []UpstreamOverrideConfig {
	if len(src) == 0 {
		return nil
	}
	dst := make([]UpstreamOverrideConfig, len(src))
	copy(dst, src)
	return dst
}

func cloneGlobalUpstreamOverrides(src GlobalUpstreamOverrides) GlobalUpstreamOverrides {
	dst := make(GlobalUpstreamOverrides, len(src))
	for pluginTag, entries := range src {
		dst[pluginTag] = cloneUpstreamList(entries)
	}
	return dst
}

func validateUpstreamEntry(u UpstreamOverrideConfig, idx int) (string, string, bool) {
	itemPos := idx + 1
	u.Tag = strings.TrimSpace(u.Tag)
	u.Protocol = strings.TrimSpace(u.Protocol)
	if u.Tag == "" {
		return "UPSTREAM_TAG_REQUIRED", fmt.Sprintf("Item #%d: tag (name) is required", itemPos), false
	}

	if !u.Enabled {
		return "", "", true
	}

	if u.Protocol == "aliapi" {
		if strings.TrimSpace(u.AccountID) == "" || strings.TrimSpace(u.AccessKeyID) == "" || strings.TrimSpace(u.AccessKeySecret) == "" {
			return "ALIAPI_CREDENTIALS_REQUIRED", fmt.Sprintf("Item #%d (%s): AliAPI requires account_id, access_key_id, and access_key_secret", itemPos, u.Tag), false
		}
		return "", "", true
	}

	if strings.TrimSpace(u.Addr) == "" {
		return "UPSTREAM_ADDR_REQUIRED", fmt.Sprintf("Item #%d (%s): addr is required for DNS types", itemPos, u.Tag), false
	}
	return "", "", true
}

func validateUpstreamList(upstreams []UpstreamOverrideConfig) (string, string, bool) {
	tagSeen := make(map[string]struct{}, len(upstreams))
	for i, u := range upstreams {
		if code, msg, ok := validateUpstreamEntry(u, i); !ok {
			return code, msg, false
		}
		tag := strings.TrimSpace(u.Tag)
		if _, duplicated := tagSeen[tag]; duplicated {
			return "UPSTREAM_TAG_DUPLICATED", fmt.Sprintf("duplicated upstream tag: %s", tag), false
		}
		tagSeen[tag] = struct{}{}
	}
	return "", "", true
}

func validateGlobalUpstreamConfig(cfg GlobalUpstreamOverrides) (string, string, bool) {
	for pluginTag, upstreams := range cfg {
		if strings.TrimSpace(pluginTag) == "" {
			return "PLUGIN_TAG_REQUIRED", "plugin_tag is required", false
		}
		if code, msg, ok := validateUpstreamList(upstreams); !ok {
			return code, msg, false
		}
	}
	return "", "", true
}

func applyUpstreamRuntimeReload(m *Mosdns, pluginTag string) error {
	if m == nil {
		return nil
	}
	return m.ReloadRuntimeConfig(pluginTag)
}

func parseQueryBool(r *http.Request, key string) bool {
	v := strings.ToLower(strings.TrimSpace(r.URL.Query().Get(key)))
	return v == "1" || v == "true" || v == "yes"
}

// handleGetAliAPITags 获取扫描到的插件 Tag
func handleGetAliAPITags(w http.ResponseWriter, r *http.Request) {
	tags := discoveredAliAPITags
	if tags == nil {
		tags = []string{}
	}
	// DEBUG
	mlog.L().Info("[Debug UpstreamAPI] API Request: Get Tags", zap.Strings("returning", tags))
	writeJSON(w, http.StatusOK, tags)
}

// handleGetUpstreamConfig 获取当前所有配置
func handleGetUpstreamConfig(w http.ResponseWriter, r *http.Request) {
	if err := ensureUpstreamOverridesLoaded(); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "UPSTREAM_CONFIG_LOAD_FAILED", "Failed to load upstream config")
		return
	}

	upstreamOverridesLock.RLock()
	safeData := make(GlobalUpstreamOverrides, len(upstreamOverrides))
	for pluginTag, entries := range upstreamOverrides {
		copied := make([]UpstreamOverrideConfig, len(entries))
		copy(copied, entries)
		safeData[pluginTag] = copied
	}
	upstreamOverridesLock.RUnlock()

	if safeData == nil {
		safeData = make(GlobalUpstreamOverrides)
	}
	writeJSON(w, http.StatusOK, safeData)
}

func handleReplaceUpstreamConfigWithMosdns(w http.ResponseWriter, r *http.Request, m *Mosdns) {
	var payload upstreamConfigReplaceRequest
	if err := decodeJSONBodyStrict(w, r, &payload, false); err != nil {
		if errors.Is(err, errJSONBodyTooLarge) {
			writeAPIError(w, http.StatusRequestEntityTooLarge, "REQUEST_BODY_TOO_LARGE", "Request body too large")
			return
		}
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST_BODY", "Invalid request body")
		return
	}

	if payload.Config == nil {
		payload.Config = make(GlobalUpstreamOverrides)
	}
	if code, msg, ok := validateGlobalUpstreamConfig(payload.Config); !ok {
		writeAPIError(w, http.StatusBadRequest, code, msg)
		return
	}

	if err := ensureUpstreamOverridesLoaded(); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "UPSTREAM_CONFIG_LOAD_FAILED", "Failed to load upstream config")
		return
	}

	if err := func() error {
		upstreamOverridesLock.Lock()
		defer upstreamOverridesLock.Unlock()
		upstreamOverrides = cloneGlobalUpstreamOverrides(payload.Config)
		return saveUpstreamOverridesLocked()
	}(); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "UPSTREAM_CONFIG_SAVE_FAILED", "Failed to save upstream config")
		return
	}

	if payload.Apply {
		if err := applyUpstreamRuntimeReload(m, ""); err != nil {
			writeAPIError(w, http.StatusInternalServerError, "UPSTREAM_RUNTIME_APPLY_FAILED", "Config saved but runtime apply failed: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"message": "上游配置已保存并生效。"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"message": "上游配置已保存。"})
}

func handleApplyUpstreamConfigWithMosdns(w http.ResponseWriter, r *http.Request, m *Mosdns) {
	if m == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "service unavailable")
		return
	}

	var payload upstreamApplyRequest
	if r.Body != nil && r.Body != http.NoBody {
		if err := decodeJSONBodyStrict(w, r, &payload, false); err != nil {
			if errors.Is(err, errJSONBodyTooLarge) {
				writeAPIError(w, http.StatusRequestEntityTooLarge, "REQUEST_BODY_TOO_LARGE", "Request body too large")
				return
			}
			writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST_BODY", "Invalid request body")
			return
		}
	}

	if err := applyUpstreamRuntimeReload(m, strings.TrimSpace(payload.PluginTag)); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "UPSTREAM_RUNTIME_APPLY_FAILED", "Runtime apply failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "上游配置已生效。"})
}

func handleGetUpstreamItems(w http.ResponseWriter, r *http.Request) {
	pluginTag := strings.TrimSpace(r.URL.Query().Get("plugin_tag"))
	if pluginTag == "" {
		writeAPIError(w, http.StatusBadRequest, "PLUGIN_TAG_REQUIRED", "plugin_tag is required")
		return
	}
	if err := ensureUpstreamOverridesLoaded(); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "UPSTREAM_CONFIG_LOAD_FAILED", "Failed to load upstream config")
		return
	}

	upstreamOverridesLock.RLock()
	items := cloneUpstreamList(upstreamOverrides[pluginTag])
	upstreamOverridesLock.RUnlock()
	if items == nil {
		items = make([]UpstreamOverrideConfig, 0)
	}
	writeJSON(w, http.StatusOK, items)
}

func handleCreateUpstreamItemWithMosdns(w http.ResponseWriter, r *http.Request, m *Mosdns) {
	var payload upstreamItemMutationRequest
	if err := decodeJSONBodyStrict(w, r, &payload, false); err != nil {
		if errors.Is(err, errJSONBodyTooLarge) {
			writeAPIError(w, http.StatusRequestEntityTooLarge, "REQUEST_BODY_TOO_LARGE", "Request body too large")
			return
		}
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST_BODY", "Invalid request body")
		return
	}
	payload.PluginTag = strings.TrimSpace(payload.PluginTag)
	if payload.PluginTag == "" {
		writeAPIError(w, http.StatusBadRequest, "PLUGIN_TAG_REQUIRED", "plugin_tag is required")
		return
	}
	if code, msg, ok := validateUpstreamList([]UpstreamOverrideConfig{payload.Upstream}); !ok {
		writeAPIError(w, http.StatusBadRequest, code, msg)
		return
	}
	if err := ensureUpstreamOverridesLoaded(); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "UPSTREAM_CONFIG_LOAD_FAILED", "Failed to load upstream config")
		return
	}

	if err := func() error {
		upstreamOverridesLock.Lock()
		defer upstreamOverridesLock.Unlock()

		if upstreamOverrides == nil {
			upstreamOverrides = make(GlobalUpstreamOverrides)
		}
		list := cloneUpstreamList(upstreamOverrides[payload.PluginTag])
		for _, existing := range list {
			if existing.Tag == payload.Upstream.Tag {
				return fmt.Errorf("duplicated upstream tag: %s", payload.Upstream.Tag)
			}
		}
		list = append(list, payload.Upstream)
		if code, msg, ok := validateUpstreamList(list); !ok {
			return fmt.Errorf("%s|%s", code, msg)
		}
		upstreamOverrides[payload.PluginTag] = list
		return saveUpstreamOverridesLocked()
	}(); err != nil {
		if strings.Contains(err.Error(), "|") {
			parts := strings.SplitN(err.Error(), "|", 2)
			writeAPIError(w, http.StatusBadRequest, parts[0], parts[1])
			return
		}
		if strings.Contains(err.Error(), "duplicated upstream tag:") {
			writeAPIError(w, http.StatusConflict, "UPSTREAM_TAG_DUPLICATED", err.Error())
			return
		}
		writeAPIError(w, http.StatusInternalServerError, "UPSTREAM_CONFIG_SAVE_FAILED", "Failed to save upstream config")
		return
	}

	if payload.Apply {
		if err := applyUpstreamRuntimeReload(m, payload.PluginTag); err != nil {
			writeAPIError(w, http.StatusInternalServerError, "UPSTREAM_RUNTIME_APPLY_FAILED", "Config saved but runtime apply failed: "+err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, map[string]string{"message": "上游已新增并生效。"})
		return
	}

	writeJSON(w, http.StatusCreated, map[string]string{"message": "上游已新增。"})
}

func handleUpdateUpstreamItemWithMosdns(w http.ResponseWriter, r *http.Request, m *Mosdns) {
	upstreamTag := strings.TrimSpace(chi.URLParam(r, "upstreamTag"))
	if upstreamTag == "" {
		writeAPIError(w, http.StatusBadRequest, "UPSTREAM_TAG_REQUIRED", "upstream tag is required")
		return
	}

	var payload upstreamItemMutationRequest
	if err := decodeJSONBodyStrict(w, r, &payload, false); err != nil {
		if errors.Is(err, errJSONBodyTooLarge) {
			writeAPIError(w, http.StatusRequestEntityTooLarge, "REQUEST_BODY_TOO_LARGE", "Request body too large")
			return
		}
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST_BODY", "Invalid request body")
		return
	}
	payload.PluginTag = strings.TrimSpace(payload.PluginTag)
	if payload.PluginTag == "" {
		writeAPIError(w, http.StatusBadRequest, "PLUGIN_TAG_REQUIRED", "plugin_tag is required")
		return
	}
	if strings.TrimSpace(payload.Upstream.Tag) == "" {
		payload.Upstream.Tag = upstreamTag
	}
	if code, msg, ok := validateUpstreamList([]UpstreamOverrideConfig{payload.Upstream}); !ok {
		writeAPIError(w, http.StatusBadRequest, code, msg)
		return
	}
	if err := ensureUpstreamOverridesLoaded(); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "UPSTREAM_CONFIG_LOAD_FAILED", "Failed to load upstream config")
		return
	}

	notFound := false
	conflictErr := false
	if err := func() error {
		upstreamOverridesLock.Lock()
		defer upstreamOverridesLock.Unlock()

		list := cloneUpstreamList(upstreamOverrides[payload.PluginTag])
		if len(list) == 0 {
			notFound = true
			return nil
		}
		idx := -1
		for i, item := range list {
			if item.Tag == upstreamTag {
				idx = i
				break
			}
		}
		if idx < 0 {
			notFound = true
			return nil
		}
		for i, item := range list {
			if i != idx && item.Tag == payload.Upstream.Tag {
				conflictErr = true
				return nil
			}
		}
		list[idx] = payload.Upstream
		if code, msg, ok := validateUpstreamList(list); !ok {
			return fmt.Errorf("%s|%s", code, msg)
		}
		upstreamOverrides[payload.PluginTag] = list
		return saveUpstreamOverridesLocked()
	}(); err != nil {
		if strings.Contains(err.Error(), "|") {
			parts := strings.SplitN(err.Error(), "|", 2)
			writeAPIError(w, http.StatusBadRequest, parts[0], parts[1])
			return
		}
		writeAPIError(w, http.StatusInternalServerError, "UPSTREAM_CONFIG_SAVE_FAILED", "Failed to save upstream config")
		return
	}
	if notFound {
		writeAPIError(w, http.StatusNotFound, "UPSTREAM_NOT_FOUND", "upstream not found")
		return
	}
	if conflictErr {
		writeAPIError(w, http.StatusConflict, "UPSTREAM_TAG_DUPLICATED", "duplicated upstream tag")
		return
	}

	if payload.Apply {
		if err := applyUpstreamRuntimeReload(m, payload.PluginTag); err != nil {
			writeAPIError(w, http.StatusInternalServerError, "UPSTREAM_RUNTIME_APPLY_FAILED", "Config saved but runtime apply failed: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"message": "上游已更新并生效。"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "上游已更新。"})
}

func handleDeleteUpstreamItemWithMosdns(w http.ResponseWriter, r *http.Request, m *Mosdns) {
	upstreamTag := strings.TrimSpace(chi.URLParam(r, "upstreamTag"))
	pluginTag := strings.TrimSpace(r.URL.Query().Get("plugin_tag"))
	if pluginTag == "" {
		writeAPIError(w, http.StatusBadRequest, "PLUGIN_TAG_REQUIRED", "plugin_tag is required")
		return
	}
	if upstreamTag == "" {
		writeAPIError(w, http.StatusBadRequest, "UPSTREAM_TAG_REQUIRED", "upstream tag is required")
		return
	}
	if err := ensureUpstreamOverridesLoaded(); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "UPSTREAM_CONFIG_LOAD_FAILED", "Failed to load upstream config")
		return
	}

	deleted := false
	if err := func() error {
		upstreamOverridesLock.Lock()
		defer upstreamOverridesLock.Unlock()

		list := cloneUpstreamList(upstreamOverrides[pluginTag])
		if len(list) == 0 {
			return nil
		}

		nextList := make([]UpstreamOverrideConfig, 0, len(list))
		for _, item := range list {
			if !deleted && item.Tag == upstreamTag {
				deleted = true
				continue
			}
			nextList = append(nextList, item)
		}
		if !deleted {
			return nil
		}
		upstreamOverrides[pluginTag] = nextList
		return saveUpstreamOverridesLocked()
	}(); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "UPSTREAM_CONFIG_SAVE_FAILED", "Failed to save upstream config")
		return
	}
	if !deleted {
		writeAPIError(w, http.StatusNotFound, "UPSTREAM_NOT_FOUND", "upstream not found")
		return
	}

	if parseQueryBool(r, "apply") {
		if err := applyUpstreamRuntimeReload(m, pluginTag); err != nil {
			writeAPIError(w, http.StatusInternalServerError, "UPSTREAM_RUNTIME_APPLY_FAILED", "Config saved but runtime apply failed: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"message": "上游已删除并生效。"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "上游已删除。"})
}

// handleSetUpstreamConfig 核心保存逻辑
func handleSetUpstreamConfig(w http.ResponseWriter, r *http.Request) {
	handleSetUpstreamConfigWithMosdns(w, r, nil)
}

func handleSetUpstreamConfigWithMosdns(w http.ResponseWriter, r *http.Request, m *Mosdns) {
	mlog.L().Info("[Debug UpstreamAPI] API Request: Set Config Received") // DEBUG

	var payload struct {
		PluginTag string                   `json:"plugin_tag"`
		Upstreams []UpstreamOverrideConfig `json:"upstreams"`
	}

	if err := decodeJSONBodyStrict(w, r, &payload, false); err != nil {
		if errors.Is(err, errJSONBodyTooLarge) {
			writeAPIError(w, http.StatusRequestEntityTooLarge, "REQUEST_BODY_TOO_LARGE", "Request body too large")
			return
		}
		mlog.L().Error("[Debug UpstreamAPI] Invalid request body", zap.Error(err))
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST_BODY", "Invalid request body")
		return
	}

	// DEBUG: 打印接收到的数据
	mlog.L().Info("[Debug UpstreamAPI] Payload decoded",
		zap.String("plugin_tag", payload.PluginTag),
		zap.Int("items_count", len(payload.Upstreams)))

	if payload.PluginTag == "" {
		writeAPIError(w, http.StatusBadRequest, "PLUGIN_TAG_REQUIRED", "plugin_tag is required")
		return
	}

	if code, msg, ok := validateUpstreamList(payload.Upstreams); !ok {
		writeAPIError(w, http.StatusBadRequest, code, msg)
		return
	}

	if err := ensureUpstreamOverridesLoaded(); err != nil {
		mlog.L().Error("[Debug UpstreamAPI] Failed to load config before save", zap.Error(err))
		writeAPIError(w, http.StatusInternalServerError, "UPSTREAM_CONFIG_LOAD_FAILED", "Failed to load config file")
		return
	}

	// Only hold the lock for in-memory mutation and file persistence.
	// Runtime reload may read upstream overrides again, so it must run after unlocking.
	if err := func() error {
		upstreamOverridesLock.Lock()
		defer upstreamOverridesLock.Unlock()

		if upstreamOverrides == nil {
			upstreamOverrides = make(GlobalUpstreamOverrides)
		}
		upstreamOverrides[payload.PluginTag] = payload.Upstreams
		return saveUpstreamOverridesLocked()
	}(); err != nil {
		mlog.L().Error("[Debug UpstreamAPI] Save failed", zap.Error(err))
		writeAPIError(w, http.StatusInternalServerError, "UPSTREAM_CONFIG_SAVE_FAILED", "Failed to save config file")
		return
	}

	if m != nil {
		if err := m.ReloadRuntimeConfig(payload.PluginTag); err != nil {
			writeAPIError(w, http.StatusInternalServerError, "UPSTREAM_RUNTIME_APPLY_FAILED", "Config saved but runtime apply failed: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"message": "上游配置已保存并生效。"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"message": "上游配置已保存。"})
}
