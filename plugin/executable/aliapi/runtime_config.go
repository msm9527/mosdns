package aliapi

import (
	"strings"

	"go.uber.org/zap"
)

func materializeRuntimeArgs(src *Args, logger *zap.Logger) *Args {
	args := cloneArgs(src)
	if args.ServerAddr == "" {
		args.ServerAddr = defaultAliAPIServer
	}

	expandedLegacyCount := 0
	for i := range args.Upstreams {
		if !strings.EqualFold(strings.TrimSpace(args.Upstreams[i].Type), "aliapi") {
			continue
		}
		trimAliAPIUpstreamConfig(&args.Upstreams[i])
		if applyLegacyAliAPIDefaults(&args.Upstreams[i], args) {
			expandedLegacyCount++
		}
		if args.Upstreams[i].ServerAddr == "" {
			args.Upstreams[i].ServerAddr = defaultAliAPIServer
		}
	}

	if expandedLegacyCount > 0 && logger != nil {
		logger.Info("expanded legacy aliapi global credentials to per-upstream config",
			zap.Int("expanded_upstreams", expandedLegacyCount))
	}

	return args
}

func trimAliAPIUpstreamConfig(c *UpstreamConfig) {
	c.AccountID = strings.TrimSpace(c.AccountID)
	c.AccessKeyID = strings.TrimSpace(c.AccessKeyID)
	c.AccessKeySecret = strings.TrimSpace(c.AccessKeySecret)
	c.ServerAddr = strings.TrimSpace(c.ServerAddr)
	c.EcsClientIP = strings.TrimSpace(c.EcsClientIP)
}

func applyLegacyAliAPIDefaults(dst *UpstreamConfig, legacy *Args) bool {
	expanded := false
	expanded = setDefaultTrimmedString(&dst.AccountID, legacy.AccountID) || expanded
	expanded = setDefaultTrimmedString(&dst.AccessKeyID, legacy.AccessKeyID) || expanded
	expanded = setDefaultTrimmedString(&dst.AccessKeySecret, legacy.AccessKeySecret) || expanded
	expanded = setDefaultTrimmedString(&dst.ServerAddr, legacy.ServerAddr) || expanded
	expanded = setDefaultTrimmedString(&dst.EcsClientIP, legacy.EcsClientIP) || expanded

	if dst.EcsClientMask == 0 && legacy.EcsClientMask > 0 {
		dst.EcsClientMask = legacy.EcsClientMask
		expanded = true
	}

	return expanded
}

func setDefaultTrimmedString(dst *string, fallback string) bool {
	if strings.TrimSpace(*dst) != "" {
		return false
	}
	trimmed := strings.TrimSpace(fallback)
	if trimmed == "" {
		return false
	}
	*dst = trimmed
	return true
}
