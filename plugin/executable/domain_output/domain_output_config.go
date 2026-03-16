package domain_output

import "strings"

const defaultPersistIntervalSeconds = 60

type Args struct {
	PublishTo       string      `yaml:"publish_to"`
	MaxEntries      int         `yaml:"max_entries"`
	PersistInterval int         `yaml:"persist_interval"`
	EnableFlags     bool        `yaml:"enable_flags"`
	Policy          *PolicyArgs `yaml:"policy"`
}

type PolicyArgs struct {
	Kind                   string `yaml:"kind"`
	PromoteAfter           int    `yaml:"promote_after"`
	DecayDays              int    `yaml:"decay_days"`
	TrackQType             bool   `yaml:"track_qtype"`
	PublishMode            string `yaml:"publish_mode"`
	StaleAfterMinutes      int    `yaml:"stale_after_minutes"`
	RefreshCooldownMinutes int    `yaml:"refresh_cooldown_minutes"`
	RequeryTag             string `yaml:"requery_tag"`
}

type writePolicy struct {
	kind                   string
	promoteAfter           int
	decayDays              int
	trackQType             bool
	publishMode            string
	staleAfterMinutes      int
	refreshCooldownMinutes int
	requeryTag             string
}

func normalizePolicy(pluginTag string, cfg *Args) writePolicy {
	kind := "generic"
	promoteAfter := 1
	decayDays := 30
	publishMode := "all"
	trackQType := false
	staleAfterMinutes := 0
	refreshCooldownMinutes := 120
	requeryTag := ""

	switch inferDomainOutputHint(pluginTag, cfg) {
	case "realip":
		kind = "realip"
		promoteAfter = 2
		decayDays = 21
		publishMode = "promoted_only"
		trackQType = true
		staleAfterMinutes = 360
		requeryTag = "requery"
	case "fakeip":
		kind = "fakeip"
		promoteAfter = 2
		decayDays = 21
		publishMode = "promoted_only"
		trackQType = true
		staleAfterMinutes = 240
		requeryTag = "requery"
	case "nodenov4", "nov4":
		kind = "nov4"
		promoteAfter = 2
		decayDays = 14
		publishMode = "promoted_only"
		trackQType = true
		staleAfterMinutes = 180
		requeryTag = "requery"
	case "nodenov6", "nov6":
		kind = "nov6"
		promoteAfter = 2
		decayDays = 14
		publishMode = "promoted_only"
		trackQType = true
		staleAfterMinutes = 180
		requeryTag = "requery"
	}

	if cfg.Policy != nil {
		if cfg.Policy.Kind != "" {
			kind = strings.ToLower(strings.TrimSpace(cfg.Policy.Kind))
		}
		if cfg.Policy.PromoteAfter > 0 {
			promoteAfter = cfg.Policy.PromoteAfter
		}
		if cfg.Policy.DecayDays > 0 {
			decayDays = cfg.Policy.DecayDays
		}
		if cfg.Policy.PublishMode != "" {
			publishMode = strings.ToLower(strings.TrimSpace(cfg.Policy.PublishMode))
		}
		if cfg.Policy.TrackQType {
			trackQType = true
		}
		if cfg.Policy.StaleAfterMinutes > 0 {
			staleAfterMinutes = cfg.Policy.StaleAfterMinutes
		}
		if cfg.Policy.RefreshCooldownMinutes > 0 {
			refreshCooldownMinutes = cfg.Policy.RefreshCooldownMinutes
		}
		if cfg.Policy.RequeryTag != "" {
			requeryTag = strings.TrimSpace(cfg.Policy.RequeryTag)
		}
	}

	if strings.TrimSpace(cfg.PublishTo) == "" {
		requeryTag = ""
	}
	return writePolicy{
		kind:                   kind,
		promoteAfter:           promoteAfter,
		decayDays:              decayDays,
		trackQType:             trackQType,
		publishMode:            publishMode,
		staleAfterMinutes:      staleAfterMinutes,
		refreshCooldownMinutes: refreshCooldownMinutes,
		requeryTag:             requeryTag,
	}
}

func inferMemoryID(pluginTag string, cfg *Args) string {
	switch inferDomainOutputHint(pluginTag, cfg) {
	case "realip":
		return "realip"
	case "fakeip":
		return "fakeip"
	case "nodenov4":
		return "nodenov4"
	case "nodenov6":
		return "nodenov6"
	case "nov4":
		return "nov4"
	case "nov6":
		return "nov6"
	case "top":
		return "top"
	default:
		return "generic"
	}
}

func inferDomainOutputHint(pluginTag string, cfg *Args) string {
	joined := strings.ToLower(strings.Join([]string{pluginTag, cfg.PublishTo}, " "))
	switch {
	case strings.Contains(joined, "realip"):
		return "realip"
	case strings.Contains(joined, "fakeip"):
		return "fakeip"
	case strings.Contains(joined, "nodenov4"):
		return "nodenov4"
	case strings.Contains(joined, "nodenov6"):
		return "nodenov6"
	case strings.Contains(joined, "nov4"):
		return "nov4"
	case strings.Contains(joined, "nov6"):
		return "nov6"
	case strings.Contains(joined, "top_domains"), strings.Contains(joined, " top "):
		return "top"
	default:
		return "generic"
	}
}
