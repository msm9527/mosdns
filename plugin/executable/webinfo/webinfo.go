package webinfo

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"sync"

	// FIX: Corrected the typo in the import path.
	"github.com/IrineSistiana/mosdns/v5/coremain"
)

const (
	PluginType                   = "webinfo"
	runtimeStateNamespaceWebinfo = "webinfo"
)

// 注册插件
func init() {
	coremain.RegNewPluginFunc(PluginType, newWebinfo, func() any { return new(Args) })
}

// Args 是插件的配置参数
type Args struct {
	File string `yaml:"file"`
}

// WebInfo 是插件的主结构体
type WebInfo struct {
	mu       sync.RWMutex
	filePath string
	// data stores arbitrary JSON/YAML-compatible payloads.
	data interface{}
}

// newWebinfo 是插件的初始化函数
func newWebinfo(_ *coremain.BP, args any) (any, error) {
	cfg := args.(*Args)
	if cfg.File == "" {
		return nil, errors.New("webinfo: 'file' must be specified")
	}

	p := &WebInfo{
		filePath: cfg.File,
	}

	if err := p.loadData(); err != nil {
		return nil, fmt.Errorf("webinfo: failed to load initial data from %s: %w", p.filePath, err)
	}
	log.Printf("[webinfo] plugin instance created for state key: %s", p.filePath)

	return p, nil
}

// loadData 从数据库加载 JSON 数据到内存
func (p *WebInfo) loadData() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if key := p.runtimeStateKey(); key != "" {
		dbPath := coremain.RuntimeStateDBPathForPath(p.filePath)
		var d interface{}
		ok, err := coremain.LoadRuntimeStateJSONFromPath(dbPath, runtimeStateNamespaceWebinfo, key, &d)
		if err == nil && ok {
			if d == nil {
				p.data = make(map[string]interface{})
			} else {
				p.data = d
			}
			return nil
		}
		if err != nil {
			return err
		}
	}
	log.Printf("[webinfo] runtime state %s not found, initializing with empty data.", p.filePath)
	p.data = make(map[string]interface{})
	return nil
}

// saveData 将内存中的数据保存到数据库
func (p *WebInfo) saveData() error {
	if key := p.runtimeStateKey(); key != "" {
		dbPath := coremain.RuntimeStateDBPathForPath(p.filePath)
		if err := coremain.SaveRuntimeStateJSONToPath(dbPath, runtimeStateNamespaceWebinfo, key, p.data); err != nil {
			return err
		}
	}
	return nil
}

func (p *WebInfo) runtimeStateKey() string {
	if p.filePath == "" {
		return ""
	}
	return filepath.Clean(p.filePath)
}

func (p *WebInfo) SnapshotJSONValue() any {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.data
}

func (p *WebInfo) ReplaceJSONValue(_ context.Context, newData any) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.data = newData
	if err := p.saveData(); err != nil {
		log.Printf("[webinfo] ERROR: failed to save runtime state %s: %v", p.filePath, err)
		return err
	}
	log.Printf("[webinfo] runtime state updated successfully: %s", p.filePath)
	return nil
}
