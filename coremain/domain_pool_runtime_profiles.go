package coremain

import "strings"

func loadRuntimeDomainProfiles() ([]runtimeDomainProfile, error) {
	values, _, err := LoadMemoryPoolPoliciesFromCustomConfig()
	if err != nil {
		return nil, err
	}

	profiles := make([]runtimeDomainProfile, 0, len(values))
	for _, tag := range orderedDomainPoolPolicyKeys(values) {
		policy, err := ResolveDomainPoolPolicy(tag, values)
		if err != nil {
			return nil, err
		}
		profiles = append(profiles, runtimeDomainProfile{
			Key:  runtimeDomainProfileKey(tag, policy),
			Name: runtimeDomainProfileName(tag, policy),
			Tag:  tag,
		})
	}
	return profiles, nil
}

func RequeryTargetDomainPoolTags(requeryTag string) ([]string, error) {
	values, _, err := LoadMemoryPoolPoliciesFromCustomConfig()
	if err != nil {
		return nil, err
	}

	target := strings.TrimSpace(requeryTag)
	tags := make([]string, 0, len(values))
	for _, tag := range orderedDomainPoolPolicyKeys(values) {
		policy, err := ResolveDomainPoolPolicy(tag, values)
		if err != nil {
			return nil, err
		}
		if policy.Kind != DomainPoolKindMemory {
			continue
		}
		if strings.TrimSpace(policy.RequeryTag) != target {
			continue
		}
		tags = append(tags, tag)
	}
	return tags, nil
}

func runtimeDomainProfileKey(tag string, policy DomainPoolPolicy) string {
	switch {
	case tag == "top_domains":
		return "total"
	case policy.MemoryID != "" && policy.MemoryID != "generic":
		return policy.MemoryID
	default:
		return tag
	}
}

func runtimeDomainProfileName(tag string, policy DomainPoolPolicy) string {
	switch tag {
	case "top_domains":
		return "请求排行"
	case "my_fakeiplist":
		return "FakeIP 域名"
	case "my_realiplist":
		return "RealIP 域名"
	case "my_nov4list":
		return "无 V4 域名"
	case "my_nov6list":
		return "无 V6 域名"
	case "my_nodenov4list":
		return "节点无 V4 域名"
	case "my_nodenov6list":
		return "节点无 V6 域名"
	default:
		return tag
	}
}
