package rulesource

import (
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

type Scope string

const (
	ScopeAdguard   Scope = "adguard"
	ScopeDiversion Scope = "diversion"
)

type Behavior string

const (
	BehaviorAdguard Behavior = "adguard"
	BehaviorDomain  Behavior = "domain"
	BehaviorIPCIDR  Behavior = "ipcidr"
)

type MatchMode string

const (
	MatchModeAdguardNative MatchMode = "adguard_native"
	MatchModeDomainSet     MatchMode = "domain_set"
	MatchModeIPCIDRSet     MatchMode = "ip_cidr_set"
)

type Format string

const (
	FormatTXT   Format = "txt"
	FormatList  Format = "list"
	FormatRules Format = "rules"
	FormatJSON  Format = "json"
	FormatYAML  Format = "yaml"
	FormatSRS   Format = "srs"
	FormatMRS   Format = "mrs"
)

type SourceKind string

const (
	SourceKindLocal  SourceKind = "local"
	SourceKindRemote SourceKind = "remote"
)

type Config struct {
	Sources []Source `yaml:"sources" json:"sources"`
}

type Source struct {
	ID                  string     `yaml:"id" json:"id"`
	Name                string     `yaml:"name" json:"name"`
	BindTo              string     `yaml:"bind_to,omitempty" json:"bind_to,omitempty"`
	Enabled             bool       `yaml:"enabled" json:"enabled"`
	Behavior            Behavior   `yaml:"behavior" json:"behavior"`
	MatchMode           MatchMode  `yaml:"match_mode" json:"match_mode"`
	Format              Format     `yaml:"format" json:"format"`
	SourceKind          SourceKind `yaml:"source_kind" json:"source_kind"`
	Path                string     `yaml:"path,omitempty" json:"path,omitempty"`
	URL                 string     `yaml:"url,omitempty" json:"url,omitempty"`
	AutoUpdate          bool       `yaml:"auto_update,omitempty" json:"auto_update,omitempty"`
	UpdateIntervalHours int        `yaml:"update_interval_hours,omitempty" json:"update_interval_hours,omitempty"`
}

var sourceIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

func NormalizeConfig(cfg Config) Config {
	sources := make([]Source, 0, len(cfg.Sources))
	for _, src := range cfg.Sources {
		sources = append(sources, NormalizeSource(src))
	}
	slices.SortFunc(sources, func(a, b Source) int {
		return strings.Compare(a.ID, b.ID)
	})
	return Config{Sources: sources}
}

func NormalizeSource(src Source) Source {
	src.ID = strings.TrimSpace(src.ID)
	src.Name = strings.TrimSpace(src.Name)
	src.BindTo = strings.TrimSpace(src.BindTo)
	src.Path = strings.TrimSpace(src.Path)
	src.URL = strings.TrimSpace(src.URL)
	src.Behavior = Behavior(strings.ToLower(strings.TrimSpace(string(src.Behavior))))
	src.MatchMode = MatchMode(strings.ToLower(strings.TrimSpace(string(src.MatchMode))))
	src.Format = Format(strings.ToLower(strings.TrimSpace(string(src.Format))))
	src.SourceKind = SourceKind(strings.ToLower(strings.TrimSpace(string(src.SourceKind))))
	if src.SourceKind == SourceKindLocal {
		src.AutoUpdate = false
		src.UpdateIntervalHours = 0
	}
	return src
}

func ValidateConfig(scope Scope, cfg Config) error {
	seen := make(map[string]struct{}, len(cfg.Sources))
	for _, raw := range cfg.Sources {
		src := NormalizeSource(raw)
		if _, ok := seen[src.ID]; ok {
			return fmt.Errorf("duplicate source id %q", src.ID)
		}
		seen[src.ID] = struct{}{}
		if err := ValidateSource(scope, src); err != nil {
			return fmt.Errorf("source %q: %w", src.ID, err)
		}
	}
	return nil
}

func ValidateSource(scope Scope, src Source) error {
	if !sourceIDPattern.MatchString(src.ID) {
		return fmt.Errorf("invalid id")
	}
	if src.Name == "" {
		return fmt.Errorf("name is required")
	}
	if err := validateScope(scope); err != nil {
		return err
	}
	if err := validateBindTo(scope, src.BindTo); err != nil {
		return err
	}
	if err := validateBehavior(scope, src.Behavior, src.MatchMode); err != nil {
		return err
	}
	if !validFormat(src.Format) {
		return fmt.Errorf("unsupported format %q", src.Format)
	}
	if src.SourceKind != SourceKindLocal && src.SourceKind != SourceKindRemote {
		return fmt.Errorf("unsupported source_kind %q", src.SourceKind)
	}
	if src.SourceKind == SourceKindRemote && src.URL == "" {
		return fmt.Errorf("url is required for remote source")
	}
	if src.SourceKind == SourceKindLocal && src.Path == "" {
		return fmt.Errorf("path is required for local source")
	}
	if src.SourceKind == SourceKindRemote && src.AutoUpdate && src.UpdateIntervalHours < 1 {
		return fmt.Errorf("update_interval_hours must be >= 1 when auto_update is enabled")
	}
	if _, err := NormalizeRelativePath(src.Path); err != nil {
		return err
	}
	return validateFormatForMode(src.Format, src.MatchMode)
}

func DefaultRelativePath(scope Scope, src Source) string {
	name := src.ID + src.Format.Extension()
	return filepath.Join(string(scope), name)
}

func validFormat(format Format) bool {
	switch format {
	case FormatTXT, FormatList, FormatRules, FormatJSON, FormatYAML, FormatSRS, FormatMRS:
		return true
	default:
		return false
	}
}

func validateScope(scope Scope) error {
	if scope == ScopeAdguard || scope == ScopeDiversion {
		return nil
	}
	return fmt.Errorf("unsupported scope %q", scope)
}

func validateBehavior(scope Scope, behavior Behavior, mode MatchMode) error {
	switch scope {
	case ScopeAdguard:
		if behavior == BehaviorAdguard && mode == MatchModeAdguardNative {
			return nil
		}
		if behavior == BehaviorDomain && mode == MatchModeDomainSet {
			return nil
		}
	case ScopeDiversion:
		if behavior == BehaviorDomain && mode == MatchModeDomainSet {
			return nil
		}
		if behavior == BehaviorIPCIDR && mode == MatchModeIPCIDRSet {
			return nil
		}
	}
	return fmt.Errorf("behavior %q does not match mode %q", behavior, mode)
}

func validateBindTo(scope Scope, bindTo string) error {
	switch scope {
	case ScopeAdguard:
		if bindTo != "" {
			return fmt.Errorf("bind_to is not supported for adguard scope")
		}
	case ScopeDiversion:
		if !sourceIDPattern.MatchString(bindTo) {
			return fmt.Errorf("bind_to is required for diversion scope")
		}
	default:
		return fmt.Errorf("unsupported scope %q", scope)
	}
	return nil
}

func validateFormatForMode(format Format, mode MatchMode) error {
	switch mode {
	case MatchModeAdguardNative:
		switch format {
		case FormatTXT, FormatList, FormatRules, FormatJSON, FormatYAML:
			return nil
		}
	case MatchModeDomainSet:
		switch format {
		case FormatTXT, FormatList, FormatRules, FormatJSON, FormatYAML, FormatSRS, FormatMRS:
			return nil
		}
	case MatchModeIPCIDRSet:
		switch format {
		case FormatTXT, FormatList, FormatJSON, FormatYAML, FormatSRS, FormatMRS:
			return nil
		}
	}
	return fmt.Errorf("format %q is not valid for match_mode %q", format, mode)
}

func (f Format) Extension() string {
	switch f {
	case FormatTXT:
		return ".txt"
	case FormatList:
		return ".list"
	case FormatRules:
		return ".rules"
	case FormatJSON:
		return ".json"
	case FormatYAML:
		return ".yaml"
	case FormatSRS:
		return ".srs"
	case FormatMRS:
		return ".mrs"
	default:
		return ".txt"
	}
}
