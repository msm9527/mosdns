package coremain

import (
	"strings"
	"time"
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
	return &RuleAPIError{
		Status:  status,
		Code:    code,
		Message: message,
	}
}

type AdguardRuleItem struct {
	ID                  string    `json:"id"`
	Name                string    `json:"name"`
	URL                 string    `json:"url"`
	Enabled             bool      `json:"enabled"`
	AutoUpdate          bool      `json:"auto_update"`
	UpdateIntervalHours int       `json:"update_interval_hours"`
	RuleCount           int       `json:"rule_count"`
	LastUpdated         time.Time `json:"last_updated"`
}

type AdguardRuleController interface {
	ListAdguardRules() ([]AdguardRuleItem, error)
	CreateAdguardRule(AdguardRuleItem) (AdguardRuleItem, error)
	UpdateAdguardRule(id string, rule AdguardRuleItem) (AdguardRuleItem, error)
	DeleteAdguardRule(id string) error
	TriggerAdguardUpdate() error
}

type DiversionRuleItem struct {
	Name                string    `json:"name"`
	Type                string    `json:"type"`
	Files               string    `json:"files"`
	URL                 string    `json:"url"`
	Enabled             bool      `json:"enabled"`
	EnableRegexp        bool      `json:"enable_regexp,omitempty"`
	AutoUpdate          bool      `json:"auto_update"`
	UpdateIntervalHours int       `json:"update_interval_hours"`
	RuleCount           int       `json:"rule_count"`
	LastUpdated         time.Time `json:"last_updated"`
}

type DiversionRuleController interface {
	ListDiversionRules() ([]DiversionRuleItem, error)
	UpsertDiversionRule(name string, rule DiversionRuleItem) (DiversionRuleItem, bool, error)
	DeleteDiversionRule(name string) error
	TriggerDiversionRuleUpdate(name string) error
}

func NormalizeRuleTypeFromTag(tag string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(tag), "_", ""))
}

