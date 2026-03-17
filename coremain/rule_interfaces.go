package coremain

import (
	"time"

	"github.com/IrineSistiana/mosdns/v5/pkg/rulesource"
)

type RuleAPIError struct {
	Status  int
	Code    string
	Message string
}

func (e *RuleAPIError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

func NewRuleAPIError(status int, code, message string) error {
	return &RuleAPIError{Status: status, Code: code, Message: message}
}

type RuleSourceItem struct {
	ID                  string                `json:"id"`
	Name                string                `json:"name"`
	BindTo              string                `json:"bind_to,omitempty"`
	Bindings            []string              `json:"bindings,omitempty"`
	Enabled             bool                  `json:"enabled"`
	Behavior            rulesource.Behavior   `json:"behavior"`
	MatchMode           rulesource.MatchMode  `json:"match_mode"`
	Format              rulesource.Format     `json:"format"`
	SourceKind          rulesource.SourceKind `json:"source_kind"`
	Path                string                `json:"path"`
	URL                 string                `json:"url,omitempty"`
	AutoUpdate          bool                  `json:"auto_update"`
	UpdateIntervalHours int                   `json:"update_interval_hours"`
	RuleCount           int                   `json:"rule_count"`
	LastUpdated         time.Time             `json:"last_updated"`
	LastError           string                `json:"last_error,omitempty"`
}
