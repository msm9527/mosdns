package domain_mapper

import "strings"

func mergeTagStrings(current, next string) string {
	if next == "" || current == next {
		return current
	}
	if current == "" {
		return next
	}
	for tag := range strings.SplitSeq(next, "|") {
		if tag != "" && !containsDomainTag(current, tag) {
			current += "|" + tag
		}
	}
	return current
}

func containsDomainTag(tags, target string) bool {
	for tag := range strings.SplitSeq(tags, "|") {
		if tag == target {
			return true
		}
	}
	return false
}
