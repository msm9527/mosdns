package coremain

import "unicode/utf8"

// estimateAuditBufferBytes reserves both owned records and transient query
// representations. Answers can expand sixfold in JSON, and the TEMP mirror,
// SQL bindings, decoded results and response encoder may coexist. This is a
// conservative flush trigger, not an upper bound on process RSS or query results.
func estimateAuditBufferBytes(log AuditLog) int64 {
	size := estimateAuditLogBytes(log)
	jsonBytes := int64(2)
	for _, answer := range log.Answers {
		// Object keys, punctuation and the largest uint32 TTL fit within 64 bytes.
		jsonBytes += 64 + auditJSONStringBudget(answer.Type) + auditJSONStringBudget(answer.Data)
		size += 2 * int64(len(answer.Type)+len(answer.Data)+2)
	}
	return size + 4*jsonBytes
}

// Count an upper bound without allocating the serialized answer at admission.
func auditJSONStringBudget(value string) int64 {
	size := int64(2)
	for _, r := range value {
		switch {
		case r == '"' || r == '\\':
			size += 2
		case r < 0x20 || r == '<' || r == '>' || r == '&' || r == '\u2028' || r == '\u2029' || r == utf8.RuneError:
			size += 6
		default:
			size += int64(utf8.RuneLen(r))
		}
	}
	return size
}
