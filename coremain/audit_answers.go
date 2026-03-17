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

func answerIPsText(answers []AnswerDetail) string {
	if len(answers) == 0 {
		return ""
	}
	values := make([]string, 0, len(answers))
	for _, answer := range answers {
		if answer.Type != "A" && answer.Type != "AAAA" {
			continue
		}
		if answer.Data == "" {
			continue
		}
		values = append(values, answer.Data)
	}
	return wrapExactSet(values)
}

func answerCNAMEsText(answers []AnswerDetail) string {
	if len(answers) == 0 {
		return ""
	}
	values := make([]string, 0, len(answers))
	for _, answer := range answers {
		if answer.Type != "CNAME" || answer.Data == "" {
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
	return "%\n" + value + "\n%"
}
