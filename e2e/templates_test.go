package e2e_test

import (
	"fmt"
	"os"
	"path/filepath"
)

const serviceE2EDataSourcesRelPath = "sub_config/20-data-sources.yaml"

func writeServiceE2EFiles(baseDir string, ports serviceE2EPorts, stubs serviceE2EUpstreams) (string, error) {
	if err := writeServiceE2EConfig(baseDir, ports, stubs); err != nil {
		return "", err
	}
	if err := writeServiceE2EControlFiles(baseDir); err != nil {
		return "", err
	}
	if err := writeServiceE2ERuleFiles(baseDir); err != nil {
		return "", err
	}
	return filepath.Join(baseDir, "config.yaml"), nil
}

func writeServiceE2EConfig(baseDir string, ports serviceE2EPorts, stubs serviceE2EUpstreams) error {
	content := fmt.Sprintf(
		serviceE2ETemplate("config.yaml.tmpl"),
		ports.api,
		filepath.Join(baseDir, "audit_logs", "audit.db"),
		stubs.domestic,
		stubs.foreign,
		stubs.cnfake,
		stubs.nocnfake,
		filepath.Join(baseDir, "rule", "client_ip.txt"),
		filepath.Join(baseDir, "rule", "blocklist.txt"),
		ports.dns,
		ports.dns,
		ports.probe,
	)
	if err := writeServiceE2EFile(filepath.Join(baseDir, "config.yaml"), content); err != nil {
		return err
	}
	if err := writeServiceE2EFile(filepath.Join(baseDir, serviceE2EDataSourcesRelPath), serviceE2ETemplate("data_sources.yaml")); err != nil {
		return err
	}
	return writeServiceE2ECachePolicies(baseDir)
}

func writeServiceE2EControlFiles(baseDir string) error {
	files := map[string]string{
		filepath.Join(baseDir, "custom_config", "switches.yaml"):          serviceE2ETemplate("switches.yaml"),
		filepath.Join(baseDir, "custom_config", "adguard_sources.yaml"):   serviceE2ETemplate("adguard_sources.yaml"),
		filepath.Join(baseDir, "custom_config", "diversion_sources.yaml"): serviceE2ETemplate("diversion_sources.yaml"),
		filepath.Join(baseDir, "custom_config", "global_overrides.yaml"):  "{}\n",
		filepath.Join(baseDir, "custom_config", "upstreams.yaml"):         "{}\n",
		filepath.Join(baseDir, "custom_config", "memory_pools.yaml"):      "{}\n",
	}
	for path, content := range files {
		if err := writeServiceE2EFile(path, content); err != nil {
			return err
		}
	}
	return nil
}

func writeServiceE2ERuleFiles(baseDir string) error {
	files := map[string]string{
		filepath.Join(baseDir, "adguard", "base.rules"):   "||ad.example^\n",
		filepath.Join(baseDir, "diversion", "cn.list"):    "full:cn.example\n",
		filepath.Join(baseDir, "diversion", "proxy.list"): "full:proxy.example\n",
		filepath.Join(baseDir, "rule", "blocklist.txt"):   "full:blocked.example\n",
		filepath.Join(baseDir, "rule", "client_ip.txt"):   "\n",
	}
	for path, content := range files {
		if err := writeServiceE2EFile(path, content); err != nil {
			return err
		}
	}
	return nil
}

func writeServiceE2ECachePolicies(baseDir string) error {
	content := `response:
  cache_main:
    persist: false
  cache_branch_foreign:
    persist: false
`
	return writeServiceE2EFile(filepath.Join(baseDir, "sub_config", "cache_policies.yaml"), content)
}

func serviceE2ETemplate(name string) string {
	path := filepath.Join("testdata", "service_e2e", name)
	data, err := os.ReadFile(path)
	if err != nil {
		panic(fmt.Sprintf("ReadFile(%s): %v", path, err))
	}
	return string(data)
}

func writeServiceE2EFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}
