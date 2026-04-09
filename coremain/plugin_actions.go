package coremain

import (
	"context"
	"time"
)

// SaveablePlugin is implemented by plugins that can persist their current state.
type SaveablePlugin interface {
	SaveToDisk(ctx context.Context) error
}

// FlushablePlugin is implemented by plugins that can clear runtime state.
type FlushablePlugin interface {
	FlushRuntime(ctx context.Context) error
}

// CacheRevisionProvider is implemented by rule-producing plugins that can
// expose a stable revision string for cache route signatures.
type CacheRevisionProvider interface {
	CacheRevision() string
}

// RuntimeCacheController is implemented by runtime caches that support bulk
// flush and domain purge operations.
type RuntimeCacheController interface {
	RuntimeCacheKind() string
	FlushRuntimeCache(ctx context.Context) error
	PurgeDomainsRuntimeCache(ctx context.Context, domains []string, qtypes []uint16) (int, error)
	RuntimeCacheEntryCount() int
}

// UpstreamStatsResetter is implemented by plugins that can clear in-memory
// upstream runtime stats.
type UpstreamStatsResetter interface {
	ResetUpstreamStats(ctx context.Context, upstreamTag string) (int, error)
}

// DomainVerifyPlugin is implemented by plugins that can mark a domain as verified.
type DomainVerifyPlugin interface {
	MarkDomainVerified(ctx context.Context, domain, verifiedAt string) (int, error)
}

// DomainRefreshCandidate describes a domain that is worth refreshing.
type DomainRefreshCandidate struct {
	Domain         string
	QTypeMask      uint8
	Weight         int
	MemoryID       string
	Reason         string
	RefreshState   string
	LastSeenAt     string
	LastDirtyAt    string
	LastVerifiedAt string
	CooldownUntil  string
	Promoted       bool
}

// DomainRefreshCandidateRequest controls how providers select refresh candidates.
type DomainRefreshCandidateRequest struct {
	Mode         string
	Limit        int
	IncludeDirty bool
	IncludeStale bool
	IncludeHot   bool
}

// DomainRefreshCandidateProvider is implemented by plugins that can expose
// prioritized refresh candidates from runtime state.
type DomainRefreshCandidateProvider interface {
	SnapshotRefreshCandidates(req DomainRefreshCandidateRequest) []DomainRefreshCandidate
}

type DomainRefreshJob struct {
	Domain     string
	MemoryID   string
	QTypeMask  uint8
	Reason     string
	VerifyTag  string
	ObservedAt time.Time
}

type DomainRefreshJobEnqueuer interface {
	EnqueueDomainRefresh(ctx context.Context, job DomainRefreshJob) bool
}
