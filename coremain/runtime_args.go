package coremain

import "gopkg.in/yaml.v3"

// DecodeRawArgsWithGlobalOverrides clones raw plugin args into out and applies
// current global overrides using YAML tags, so field names like socks5/files
// are preserved during the transform.
func DecodeRawArgsWithGlobalOverrides(tag string, raw any, out any, global *GlobalOverrides) error {
	data, err := yaml.Marshal(raw)
	if err != nil {
		return err
	}

	var generic any
	if err := yaml.Unmarshal(data, &generic); err != nil {
		return err
	}

	generic = applyRecursive(tag, generic, global)

	data, err = yaml.Marshal(generic)
	if err != nil {
		return err
	}
	return yaml.Unmarshal(data, out)
}
