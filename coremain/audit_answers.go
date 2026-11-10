package coremain

import (
	"encoding/json"
	"strings"
)

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
	// answer_search_text stores one answer per line. Escape LIKE metacharacters
	// so AuditMatchExact remains literal even for values such as "%_".
	value = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(value)
	return "%\n" + value + "\n%"
}
