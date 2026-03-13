package webinfo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
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
	// Replaced 'any' with 'interface{}' for backward compatibility.
	data interface{}
}

// newWebinfo 是插件的初始化函数
func newWebinfo(_ *coremain.BP, args any) (any, error) {
	cfg := args.(*Args)
	if cfg.File == "" {
		return nil, errors.New("webinfo: 'file' must be specified")
	}

	dir := filepath.Dir(cfg.File)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("webinfo: failed to create directory %s: %w", dir, err)
	}

	p := &WebInfo{
		filePath: cfg.File,
	}

	if err := p.loadData(); err != nil {
		return nil, fmt.Errorf("webinfo: failed to load initial data from %s: %w", p.filePath, err)
	}
	log.Printf("[webinfo] plugin instance created for file: %s", p.filePath)

	return p, nil
}

// loadData 从文件加载 JSON 数据到内存
func (p *WebInfo) loadData() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if key := p.runtimeStateKey(); key != "" {
		var d interface{}
		ok, err := coremain.LoadRuntimeStateJSON(runtimeStateNamespaceWebinfo, key, &d)
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

	dataBytes, err := os.ReadFile(p.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			log.Printf("[webinfo] file %s not found, initializing with empty data.", p.filePath)
			p.data = make(map[string]interface{})
			return nil
		}
		return err
	}

	if len(dataBytes) == 0 {
		p.data = make(map[string]interface{})
		return nil
	}

	var d interface{}
	if err := json.Unmarshal(dataBytes, &d); err != nil {
		return fmt.Errorf("failed to parse json from file %s: %w", p.filePath, err)
	}
	p.data = d

	return nil
}

// saveData 将内存中的数据保存到文件（原子写入）
func (p *WebInfo) saveData() error {
	if key := p.runtimeStateKey(); key != "" {
		if err := coremain.SaveRuntimeStateJSON(runtimeStateNamespaceWebinfo, key, p.data); err != nil {
			return err
		}
	}

	dataBytes, err := json.MarshalIndent(p.data, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal data to json: %w", err)
	}

	tmpFile := p.filePath + ".tmp"
	if err := os.WriteFile(tmpFile, dataBytes, 0644); err != nil {
		return fmt.Errorf("failed to write to temporary file: %w", err)
	}
	if err := os.Rename(tmpFile, p.filePath); err != nil {
		_ = os.Remove(tmpFile)
		return fmt.Errorf("failed to rename temporary file to final destination: %w", err)
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
		log.Printf("[webinfo] ERROR: failed to save data to file %s: %v", p.filePath, err)
		return err
	}
	log.Printf("[webinfo] data updated successfully for file: %s", p.filePath)
	return nil
}
