package coremain

import (
	"encoding/json"
	"path/filepath"
	"strings"
)

const (
	defaultAuditStorageEngine = "sqlite"
	defaultAuditMaxDBSizeMB   = 10
	maxAuditMaxDBSizeMB       = 10240
	auditSQLiteFilename       = "audit.db"
)

type auditStorage interface {
	Name() string
	Open() error
	Close() error
	WriteBatch(logs []AuditLog) error
	LoadRecent(limit int) ([]AuditLog, error)
	QueryLogs(params V2GetLogsParams) (V2PaginatedLogsResponse, error)
	EnforceRetention(settings AuditSettings) error
	Clear() error
	DiskUsageBytes() (int64, error)
	Path() string
}

func defaultAuditSQLitePath(configBaseDir string) string {
	baseDir := configBaseDir
	if baseDir == "" {
		baseDir = "."
	}
	return filepath.Join(baseDir, auditLogsDirname, auditSQLiteFilename)
}

func resolveAuditSQLitePath(configBaseDir, configured string) string {
	configured = strings.TrimSpace(configured)
	if configured == "" {
		return defaultAuditSQLitePath(configBaseDir)
	}
	if filepath.IsAbs(configured) || configBaseDir == "" {
		return configured
	}
	return filepath.Join(configBaseDir, configured)
}

func wrapExactSet(values []string) string {
	if len(values) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteByte('\n')
	for _, value := range values {
		if value == "" {
			continue
		}
		b.WriteString(value)
		b.WriteByte('\n')
	}
	return b.String()
}

func wrapExactPattern(value string) string {
	if value == "" {
		return ""
	}
	return "%\n" + value + "\n%"
}

func answerSearchText(answers []AnswerDetail) string {
	if len(answers) == 0 {
		return ""
	}
	values := make([]string, 0, len(answers))
	for _, answer := range answers {
		if answer.Data == "" {
			continue
		}
		values = append(values, answer.Data)
	}
	return wrapExactSet(values)
}

func answerIPsText(answers []AnswerDetail) string {
	if len(answers) == 0 {
		return ""
	}
	values := make([]string, 0, len(answers))
	for _, answer := range answers {
		if (answer.Type == "A" || answer.Type == "AAAA") && answer.Data != "" {
			values = append(values, answer.Data)
		}
	}
	return wrapExactSet(values)
}

func answerCNAMEsText(answers []AnswerDetail) string {
	if len(answers) == 0 {
		return ""
	}
	values := make([]string, 0, len(answers))
	for _, answer := range answers {
		if answer.Type == "CNAME" && answer.Data != "" {
			values = append(values, answer.Data)
		}
	}
	return wrapExactSet(values)
}

func marshalAnswers(answers []AnswerDetail) string {
	if len(answers) == 0 {
		return "[]"
	}
	data, err := json.Marshal(answers)
	if err != nil {
		return "[]"
	}
	return string(data)
}
