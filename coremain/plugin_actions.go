package coremain

import "context"

// SaveablePlugin is implemented by plugins that can persist their current state.
type SaveablePlugin interface {
	SaveToDisk(ctx context.Context) error
}

// FlushablePlugin is implemented by plugins that can clear runtime state.
type FlushablePlugin interface {
	FlushRuntime(ctx context.Context) error
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
