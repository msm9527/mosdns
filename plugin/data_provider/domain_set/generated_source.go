package domain_set

import (
	"fmt"
	"strings"

	"github.com/IrineSistiana/mosdns/v5/plugin/data_provider"
	"go.uber.org/zap"
)

func (d *DomainSet) loadGeneratedRules(generatedFrom string) (data_provider.RuleExporter, []string, error) {
	exporter, ok, err := d.resolveGeneratedExporter(generatedFrom)
	if err != nil {
		return nil, nil, err
	}
	if ok {
		rules, err := exporter.GetRules()
		if err != nil {
			return nil, nil, fmt.Errorf("load rules from exporter %s: %w", generatedFrom, err)
		}
		for _, rule := range rules {
			if err := d.mixM.Add(rule, struct{}{}); err != nil {
				continue
			}
		}
		return exporter, rules, nil
	}
	return nil, nil, fmt.Errorf("generated_from source %s is unavailable", generatedFrom)
}

func (d *DomainSet) resolveGeneratedExporter(generatedFrom string) (data_provider.RuleExporter, bool, error) {
	tag := strings.TrimSpace(generatedFrom)
	if tag == "" {
		return nil, false, nil
	}
	if d.bp == nil || d.bp.M() == nil {
		return nil, false, fmt.Errorf("generated_from source %s requires a running plugin manager", tag)
	}
	pluginInterface := d.bp.M().GetPlugin(tag)
	if pluginInterface == nil {
		return nil, false, fmt.Errorf("generated_from source plugin %s not found", tag)
	}
	exporter, ok := pluginInterface.(data_provider.RuleExporter)
	if !ok {
		return nil, false, fmt.Errorf("generated_from source plugin %s does not support rule export", tag)
	}
	return exporter, true, nil
}

func (d *DomainSet) subscribeGeneratedSource(generatedFrom string, exporter data_provider.RuleExporter) {
	tag := strings.TrimSpace(generatedFrom)
	if tag == "" || exporter == nil {
		return
	}

	d.mu.Lock()
	if d.generatedSubscriptions == nil {
		d.generatedSubscriptions = make(map[string]struct{})
	}
	if _, exists := d.generatedSubscriptions[tag]; exists {
		d.mu.Unlock()
		return
	}
	d.generatedSubscriptions[tag] = struct{}{}
	d.mu.Unlock()

	exporter.Subscribe(func() {
		if err := d.reloadFromGeneratedSource(); err != nil && d.bp != nil {
			d.bp.L().Warn(
				"domain_set generated source reload failed",
				zap.String("plugin", d.pluginTag),
				zap.String("generated_from", tag),
				zap.Error(err),
			)
		}
	})
}

func (d *DomainSet) reloadFromGeneratedSource() error {
	d.mu.RLock()
	files := append([]string(nil), d.curArgs.Files...)
	d.mu.RUnlock()

	fileStates, err := collectWatchedFileStates(files)
	if err != nil {
		return err
	}
	return d.reloadCurrentArgs(fileStates)
}

func collectWatchedFileStates(files []string) (map[string]watchedFileState, error) {
	states := make(map[string]watchedFileState, len(files))
	for _, file := range files {
		state, err := statWatchedFile(file)
		if err != nil {
			return nil, err
		}
		states[file] = state
	}
	return states, nil
}
