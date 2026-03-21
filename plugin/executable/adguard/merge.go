package adguard_rule

import (
	"github.com/IrineSistiana/mosdns/v5/pkg/matcher/domain"
	"github.com/IrineSistiana/mosdns/v5/pkg/rulesource"
)

func mergeSourceRules(
	source rulesource.Source,
	data []byte,
	importantAllowMatcher *domain.MixMatcher[struct{}],
	importantDenyMatcher *domain.MixMatcher[struct{}],
	allowMatcher *domain.MixMatcher[struct{}],
	denyMatcher *domain.MixMatcher[struct{}],
	denyRules *[]string,
) error {
	if source.Behavior == rulesource.BehaviorAdguard {
		result, err := rulesource.ParseAdguardBytes(source.Format, data)
		if err != nil {
			return err
		}
		return mergeAdguardResult(
			result,
			importantAllowMatcher,
			importantDenyMatcher,
			allowMatcher,
			denyMatcher,
			denyRules,
		)
	}
	rules, err := rulesource.ParseDomainBytes(source.Format, data)
	if err != nil {
		return err
	}
	for _, rule := range rules {
		if err := denyMatcher.Add(rule, struct{}{}); err != nil {
			return err
		}
		*denyRules = append(*denyRules, rule)
	}
	return nil
}

func mergeAdguardResult(
	result rulesource.AdguardResult,
	importantAllowMatcher *domain.MixMatcher[struct{}],
	importantDenyMatcher *domain.MixMatcher[struct{}],
	allowMatcher *domain.MixMatcher[struct{}],
	denyMatcher *domain.MixMatcher[struct{}],
	denyRules *[]string,
) error {
	if err := addRulesToMatcher(importantAllowMatcher, result.ImportantAllow); err != nil {
		return err
	}
	if err := addRulesToMatcher(importantDenyMatcher, result.ImportantDeny); err != nil {
		return err
	}
	if err := addRulesToMatcher(allowMatcher, result.Allow); err != nil {
		return err
	}
	if err := addRulesToMatcher(denyMatcher, result.Deny); err != nil {
		return err
	}
	*denyRules = append(*denyRules, result.ImportantDeny...)
	*denyRules = append(*denyRules, result.Deny...)
	return nil
}

func addRulesToMatcher(matcher *domain.MixMatcher[struct{}], rules []string) error {
	for _, rule := range rules {
		if err := matcher.Add(rule, struct{}{}); err != nil {
			return err
		}
	}
	return nil
}
