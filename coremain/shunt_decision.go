package coremain

import "strings"

type shuntDecision struct {
	Stage   string `json:"stage"`
	Action  string `json:"action"`
	Reason  string `json:"reason"`
	Matched uint8  `json:"matched_mark,omitempty"`
}

type shuntDecisionStep struct {
	Order       int    `json:"order"`
	Stage       string `json:"stage"`
	Mark        uint8  `json:"mark,omitempty"`
	Action      string `json:"action"`
	Reason      string `json:"reason"`
	Matched     bool   `json:"matched"`
	RuleTag     string `json:"rule_tag,omitempty"`
	OutputTag   string `json:"output_tag,omitempty"`
	SkippedBy   string `json:"skipped_by,omitempty"`
	DecisionHit bool   `json:"decision_hit"`
}

func decideShuntAction(qtype string, marks map[uint8]bool, switches map[string]string, rules []shuntRuleConfig) (shuntDecision, []shuntDecisionStep) {
	blockResponse := switches["block_response"] != "off"
	blockQueryType := switches["block_query_type"] != "off"
	blockIPv6 := switches["block_ipv6"] == "on"
	adBlock := switches["ad_block"] == "on"
	outputTagByMark := make(map[uint8]string)
	tagByMark := make(map[uint8]string)
	for _, rule := range rules {
		if _, ok := outputTagByMark[rule.Mark]; !ok {
			outputTagByMark[rule.Mark] = rule.OutputTag
		}
		if _, ok := tagByMark[rule.Mark]; !ok {
			tagByMark[rule.Mark] = rule.Tag
		}
	}
	path := make([]shuntDecisionStep, 0, 16)
	appendStep := func(stage string, mark uint8, action, reason string, matched, hit bool) {
		path = append(path, shuntDecisionStep{
			Order:       len(path) + 1,
			Stage:       stage,
			Mark:        mark,
			Action:      action,
			Reason:      reason,
			Matched:     matched,
			RuleTag:     tagByMark[mark],
			OutputTag:   outputTagByMark[mark],
			DecisionHit: hit,
		})
	}

	switch qtype {
	case "SOA", "PTR", "HTTPS", "TYPE65":
		appendStep("precheck", 0, "reject", "检查特殊查询类型是否拦截", blockQueryType, blockQueryType)
		if blockQueryType {
			return shuntDecision{Stage: "precheck", Action: "reject", Reason: "block_query_type:on for special qtype"}, path
		}
	case "AAAA":
		appendStep("precheck", 28, "reject", "检查 IPv6 是否被 block_ipv6:on 拦截", blockIPv6, blockIPv6)
		if blockIPv6 {
			return shuntDecision{Stage: "precheck", Action: "reject", Reason: "block_ipv6:on", Matched: 28}, path
		}
	}

	appendStep("precheck", 1, "reject", "blocklist + block_response:on", marks[1] && blockResponse, marks[1] && blockResponse)
	if marks[1] && blockResponse {
		return shuntDecision{Stage: "precheck", Action: "reject", Reason: "blocklist + block_response:on", Matched: 1}, path
	}
	appendStep("precheck", 2, "reject", "记忆无V4 + block_response:on", qtype == "A" && marks[2] && blockResponse, qtype == "A" && marks[2] && blockResponse)
	if qtype == "A" && marks[2] && blockResponse {
		return shuntDecision{Stage: "precheck", Action: "reject", Reason: "记忆无V4 + block_response:on", Matched: 2}, path
	}
	appendStep("precheck", 3, "reject", "记忆无V6 + block_response:on", qtype == "AAAA" && marks[3] && blockResponse, qtype == "AAAA" && marks[3] && blockResponse)
	if qtype == "AAAA" && marks[3] && blockResponse {
		return shuntDecision{Stage: "precheck", Action: "reject", Reason: "记忆无V6 + block_response:on", Matched: 3}, path
	}
	appendStep("precheck", 5, "reject", "广告屏蔽 + ad_block:on", marks[5] && adBlock, marks[5] && adBlock)
	if marks[5] && adBlock {
		return shuntDecision{Stage: "precheck", Action: "reject", Reason: "广告屏蔽 + ad_block:on", Matched: 5}, path
	}
	appendStep("precheck", 6, "domestic", "DDNS 域名直接走国内上游", marks[6], marks[6])
	if marks[6] {
		return shuntDecision{Stage: "precheck", Action: "domestic", Reason: "DDNS 域名直接走国内上游", Matched: 6}, path
	}

	for _, step := range []struct {
		Mark   uint8
		Action string
		Reason string
	}{
		{7, "sequence_fakeip", "灰名单优先走 fakeip/代理"},
		{8, "sequence_local", "白名单走国内直连链路"},
		{11, "sequence_local_divert", "记忆直连走国内链路"},
		{12, "sequence_fakeip", "记忆代理走 fakeip/代理"},
		{13, "sequence_local", "订阅直连补充走国内链路"},
		{14, "sequence_fakeip_addlist", "订阅代理走 fakeip/代理并加入清单"},
		{15, "sequence_fakeip_addlist", "订阅代理补充走 fakeip/代理并加入清单"},
		{16, "sequence_local", "订阅直连走国内链路"},
	} {
		appendStep("sequence_known_domain", step.Mark, step.Action, step.Reason, marks[step.Mark], marks[step.Mark])
		if marks[step.Mark] {
			return shuntDecision{Stage: "sequence_known_domain", Action: step.Action, Reason: step.Reason, Matched: step.Mark}, path
		}
	}
	path = append(path, shuntDecisionStep{
		Order:       len(path) + 1,
		Stage:       "sequence_fallback",
		Action:      "not_in_list_noleak_" + strings.ToLower(qtype),
		Reason:      "未命中 known-domain 优先级，进入当前无泄漏列表外解析逻辑",
		Matched:     true,
		DecisionHit: true,
	})
	return shuntDecision{
		Stage:  "sequence_fallback",
		Action: "not_in_list_noleak_" + strings.ToLower(qtype),
		Reason: "未命中 known-domain 优先级，进入当前无泄漏列表外解析逻辑",
	}, path
}
