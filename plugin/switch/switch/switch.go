package switcher

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/IrineSistiana/mosdns/v5/pkg/query_context"
	"github.com/IrineSistiana/mosdns/v5/plugin/executable/sequence"
	"github.com/IrineSistiana/mosdns/v5/plugin/switch/switchmeta"
	"github.com/go-chi/chi/v5"
)

const PluginType = "switch"

// globalRegistry a thread-safe registry for all switch instances.
var globalRegistry = struct {
	sync.RWMutex
	instances map[string]*Switch
	apiOnce   sync.Once
}{
	instances: make(map[string]*Switch),
}

// Args defines the configuration for a switch instance.
type Args struct {
	// Name is a unique identifier for this switch instance.
	// It's used in match clauses, e.g., `switch "my_switch:on"`.
	Name string `yaml:"name"`
}

// Switch represents a single, named switch instance.
type Switch struct {
	value atomic.Value
	store *stateStore
	def   switchmeta.Definition
}

// Register the plugin with mosdns core.
func init() {
	sequence.MustRegMatchQuickSetup(PluginType, QuickSetup)
	coremain.RegNewPluginFunc(
		PluginType,
		Init,
		func() any { return new(Args) },
	)
}

// Init creates and initializes a new Switch instance based on config.
func Init(bp *coremain.BP, args any) (any, error) {
	cfg := args.(*Args)

	if cfg.Name == "" {
		return nil, fmt.Errorf("plugin '%s' requires a non-empty 'name'", PluginType)
	}
	def, ok := switchmeta.Lookup(cfg.Name)
	if !ok {
		return nil, fmt.Errorf("unknown switch name: %s", cfg.Name)
	}

	sw := &Switch{
		store: getStateStore(),
		def:   def,
	}
	if err := sw.load(); err != nil {
		return nil, err
	}

	// Register the instance to the global registry.
	globalRegistry.Lock()
	defer globalRegistry.Unlock()
	if _, exists := globalRegistry.instances[def.Name]; exists {
		return nil, fmt.Errorf("duplicate switch name detected: '%s'", def.Name)
	}
	globalRegistry.instances[def.Name] = sw

	globalRegistry.apiOnce.Do(func() {
		bp.MountAPI("/api/v1/control/switches", coreSwitchesAPI())
	})

	return sw, nil
}

func (s *Switch) Exec(ctx context.Context, qCtx *query_context.Context, next sequence.ChainWalker) error {
	return next.ExecNext(ctx, qCtx)
}

// QuickSetup parses the raw string from a match clause.
// Expected format: "switch_name:expected_value"
func QuickSetup(_ sequence.BQ, raw string) (sequence.Matcher, error) {
	cleanRaw := strings.Trim(raw, `"'`)
	parts := strings.SplitN(cleanRaw, ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return nil, fmt.Errorf("invalid switch matcher format: '%s'. Expected 'name:value'", cleanRaw)
	}
	def, ok := switchmeta.Lookup(parts[0])
	if !ok {
		return nil, fmt.Errorf("unknown switch name: %s", parts[0])
	}
	expected, err := def.NormalizeValue(parts[1])
	if err != nil {
		return nil, err
	}
	return &Matcher{name: def.Name, expectedValue: expected}, nil
}

// Matcher implements the sequence.Matcher interface.
type Matcher struct {
	name          string
	expectedValue string
}

// Match performs the actual comparison.
func (m *Matcher) Match(_ context.Context, _ *query_context.Context) (bool, error) {
	globalRegistry.RLock()
	instance, ok := globalRegistry.instances[m.name]
	globalRegistry.RUnlock()

	if !ok {
		return false, nil
	}
	return instance.GetValue() == m.expectedValue, nil
}

func (m *Matcher) GetFastCheck() func(qCtx *query_context.Context) bool {
	expected := m.expectedValue
	name := m.name
	return func(_ *query_context.Context) bool {
		globalRegistry.RLock()
		instance := globalRegistry.instances[name]
		globalRegistry.RUnlock()
		if instance == nil {
			return false
		}
		return instance.GetValue() == expected
	}
}

func (s *Switch) GetValue() string {
	if val, ok := s.value.Load().(string); ok {
		return val
	}
	return s.def.DefaultValue
}

func (s *Switch) setValue(value string) error {
	if err := s.store.Set(s.def, value); err != nil {
		return err
	}
	s.value.Store(value)
	return nil
}

func (s *Switch) load() error {
	value, err := s.store.Ensure(s.def)
	if err != nil {
		return err
	}
	s.value.Store(value)
	return nil
}

type stateStore struct {
	mu sync.Mutex
}

var sharedStateStore = &stateStore{}

func getStateStore() *stateStore {
	return sharedStateStore
}

func (s *stateStore) Ensure(def switchmeta.Definition) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	values, exists, err := s.read()
	if err != nil {
		return "", err
	}
	current, ok := values[def.Name]
	if !ok {
		current = def.DefaultValue
		values[def.Name] = current
	}
	if !exists {
		if err := s.write(values); err != nil {
			return "", err
		}
	}
	return current, nil
}

func (s *stateStore) Set(def switchmeta.Definition, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	values, _, err := s.read()
	if err != nil {
		return err
	}
	values[def.Name] = value
	return s.write(values)
}

func (s *stateStore) read() (map[string]string, bool, error) {
	values, ok, err := coremain.LoadSwitchesFromCustomConfig()
	if err != nil {
		return nil, false, err
	}
	return values, ok, nil
}

func (s *stateStore) write(values map[string]string) error {
	return coremain.SaveSwitchesToCustomConfig(values)
}

func parseIncomingValue(r *http.Request, def switchmeta.Definition) (string, error) {
	contentType := r.Header.Get("Content-Type")
	var raw string

	switch {
	case strings.HasPrefix(contentType, "application/json"):
		var body struct {
			Value string `json:"value"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			return "", fmt.Errorf("invalid JSON body")
		}
		raw = body.Value
	case strings.HasPrefix(contentType, "application/x-www-form-urlencoded"),
		strings.HasPrefix(contentType, "multipart/form-data"):
		if err := r.ParseForm(); err != nil {
			return "", fmt.Errorf("invalid form data")
		}
		raw = r.FormValue("value")
	default:
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return "", fmt.Errorf("failed to read request body")
		}
		raw = string(body)
	}
	return def.NormalizeValue(raw)
}

type switchState struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

func coreSwitchesAPI() *chi.Mux {
	r := chi.NewRouter()
	r.Get("/", handleGetAllSwitches)
	r.Get("/{name}", handleGetSwitch)
	r.Put("/{name}", handleUpdateSwitch)
	return r
}

func handleGetAllSwitches(w http.ResponseWriter, _ *http.Request) {
	payload := make([]switchState, 0, len(switchmeta.Ordered()))
	for _, def := range switchmeta.Ordered() {
		if sw := getSwitchByName(def.Name); sw != nil {
			payload = append(payload, switchState{
				Name:  def.Name,
				Value: sw.GetValue(),
			})
		}
	}
	writeSwitchJSON(w, payload)
}

func handleGetSwitch(w http.ResponseWriter, r *http.Request) {
	def, sw, ok := resolveSwitch(chi.URLParam(r, "name"))
	if !ok {
		writeSwitchErrorJSON(w, http.StatusNotFound, "SWITCH_NOT_FOUND", "switch not found")
		return
	}
	writeSwitchJSON(w, switchState{
		Name:  def.Name,
		Value: sw.GetValue(),
	})
}

func handleUpdateSwitch(w http.ResponseWriter, r *http.Request) {
	def, sw, ok := resolveSwitch(chi.URLParam(r, "name"))
	if !ok {
		writeSwitchErrorJSON(w, http.StatusNotFound, "SWITCH_NOT_FOUND", "switch not found")
		return
	}

	value, err := parseIncomingValue(r, def)
	if err != nil {
		writeSwitchErrorJSON(w, http.StatusBadRequest, "INVALID_SWITCH_VALUE", err.Error())
		return
	}
	if err := sw.setValue(value); err != nil {
		writeSwitchErrorJSON(w, http.StatusInternalServerError, "SWITCH_UPDATE_FAILED", "failed to update switch store: "+err.Error())
		return
	}
	_ = coremain.RecordSystemEvent("control.switches", "info", "updated switch value", map[string]any{
		"name":  def.Name,
		"value": value,
	})

	writeSwitchJSON(w, switchState{
		Name:  def.Name,
		Value: value,
	})
}

func resolveSwitch(name string) (switchmeta.Definition, *Switch, bool) {
	def, ok := switchmeta.Lookup(name)
	if !ok {
		return switchmeta.Definition{}, nil, false
	}
	sw := getSwitchByName(def.Name)
	if sw == nil {
		return switchmeta.Definition{}, nil, false
	}
	return def, sw, true
}

func getSwitchByName(name string) *Switch {
	globalRegistry.RLock()
	defer globalRegistry.RUnlock()
	return globalRegistry.instances[name]
}

func writeSwitchJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeSwitchErrorJSON(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"code":  code,
		"error": message,
	})
}
