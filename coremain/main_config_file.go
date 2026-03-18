package coremain

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

const defaultMainConfigFilename = "config.yaml"

var MainConfigFilePath string

func resolveMainConfigFilePath() (string, error) {
	if path := filepath.Clean(MainConfigFilePath); path != "." && path != "" {
		return path, nil
	}
	if MainConfigBaseDir == "" {
		return "", fmt.Errorf("main config base dir is empty")
	}
	return filepath.Join(MainConfigBaseDir, defaultMainConfigFilename), nil
}

func saveAuditSettingsToMainConfig(settings AuditSettings) error {
	path, err := resolveMainConfigFilePath()
	if err != nil {
		return err
	}
	return saveAuditSettingsToMainConfigPath(path, settings)
}

func resolveMainConfigFilePathForRuntime(m *Mosdns) (string, error) {
	if m != nil && m.MainConfigPath() != "" {
		return filepath.Clean(m.MainConfigPath()), nil
	}
	return resolveMainConfigFilePath()
}

func saveAuditSettingsToMainConfigPath(path string, settings AuditSettings) error {
	path = filepath.Clean(path)
	if path == "" || path == "." {
		return fmt.Errorf("main config path is empty")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read main config: %w", err)
	}
	doc, err := parseYAMLDocument(raw)
	if err != nil {
		return err
	}
	root, err := yamlDocumentMapping(doc)
	if err != nil {
		return err
	}
	updateAuditConfigNode(ensureMappingValue(root, "audit"), settings)
	return writeYAMLDocument(path, doc)
}

func parseYAMLDocument(raw []byte) (*yaml.Node, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse main config yaml: %w", err)
	}
	return &doc, nil
}

func yamlDocumentMapping(doc *yaml.Node) (*yaml.Node, error) {
	if doc == nil || len(doc.Content) == 0 {
		return nil, fmt.Errorf("main config yaml document is empty")
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("main config root must be a mapping")
	}
	return root, nil
}

func writeYAMLDocument(path string, doc *yaml.Node) error {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		_ = enc.Close()
		return fmt.Errorf("encode main config yaml: %w", err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("close main config encoder: %w", err)
	}
	return writeTextFileAtomically(path, buf.Bytes())
}

func updateAuditConfigNode(node *yaml.Node, settings AuditSettings) {
	setBoolMappingValue(node, "enabled", settings.Enabled)
	setIntMappingValue(node, "overview_window_seconds", settings.OverviewWindowSeconds)
	setIntMappingValue(node, "raw_retention_days", settings.RawRetentionDays)
	setIntMappingValue(node, "aggregate_retention_days", settings.AggregateRetentionDays)
	setIntMappingValue(node, "max_storage_mb", settings.MaxStorageMB)
	setStringMappingValue(node, "sqlite_path", settings.SQLitePath)
	setIntMappingValue(node, "flush_batch_size", settings.FlushBatchSize)
	setIntMappingValue(node, "flush_interval_ms", settings.FlushIntervalMs)
	setIntMappingValue(node, "maintenance_interval_seconds", settings.MaintenanceIntervalSeconds)
}

func ensureMappingValue(parent *yaml.Node, key string) *yaml.Node {
	if value, ok := lookupMappingValue(parent, key); ok {
		if value.Kind != yaml.MappingNode {
			value.Kind = yaml.MappingNode
			value.Tag = "!!map"
			value.Content = nil
		}
		return value
	}
	keyNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
	valueNode := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	parent.Content = append(parent.Content, keyNode, valueNode)
	return valueNode
}

func lookupMappingValue(node *yaml.Node, key string) (*yaml.Node, bool) {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil, false
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1], true
		}
	}
	return nil, false
}

func setBoolMappingValue(node *yaml.Node, key string, value bool) {
	setScalarMappingValue(node, key, "!!bool", fmt.Sprintf("%t", value))
}

func setIntMappingValue(node *yaml.Node, key string, value int) {
	setScalarMappingValue(node, key, "!!int", fmt.Sprintf("%d", value))
}

func setStringMappingValue(node *yaml.Node, key, value string) {
	setScalarMappingValue(node, key, "!!str", value)
}

func setScalarMappingValue(node *yaml.Node, key, tag, value string) {
	if current, ok := lookupMappingValue(node, key); ok {
		current.Kind = yaml.ScalarNode
		current.Tag = tag
		current.Value = value
		current.Style = 0
		return
	}
	keyNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
	valueNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: value}
	node.Content = append(node.Content, keyNode, valueNode)
}
