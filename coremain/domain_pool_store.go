package coremain

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

type DomainPoolMeta struct {
	PoolTag              string           `json:"pool_tag"`
	PoolKind             string           `json:"pool_kind"`
	MemoryID             string           `json:"memory_id,omitempty"`
	Policy               DomainPoolPolicy `json:"policy"`
	DomainCount          int              `json:"domain_count"`
	VariantCount         int              `json:"variant_count"`
	DirtyDomainCount     int              `json:"dirty_domain_count"`
	PromotedDomainCount  int              `json:"promoted_domain_count"`
	PublishedDomainCount int              `json:"published_domain_count"`
	TotalObservations    int64            `json:"total_observations"`
	DroppedObservations  int64            `json:"dropped_observations"`
	DroppedByBuffer      int64            `json:"dropped_by_buffer"`
	DroppedByCap         int64            `json:"dropped_by_cap"`
	EvictedDomains       int64            `json:"evicted_domains"`
	EvictedVariants      int64            `json:"evicted_variants"`
	LastIngestedAtUnixMS int64            `json:"last_ingested_at_unix_ms"`
	LastFlushAtUnixMS    int64            `json:"last_flush_at_unix_ms"`
	LastPublishAtUnixMS  int64            `json:"last_publish_at_unix_ms"`
	LastPruneAtUnixMS    int64            `json:"last_prune_at_unix_ms"`
	UpdatedAtUnixMS      int64            `json:"updated_at_unix_ms"`
}

type DomainPoolDomain struct {
	PoolTag              string `json:"pool_tag"`
	Domain               string `json:"domain"`
	TotalCount           int    `json:"total_count"`
	Score                int    `json:"score"`
	QTypeMask            uint8  `json:"qtype_mask"`
	FlagsMask            uint8  `json:"flags_mask"`
	VariantCount         int    `json:"variant_count"`
	DirtyVariantCount    int    `json:"dirty_variant_count"`
	Promoted             bool   `json:"promoted"`
	LastSource           string `json:"last_source,omitempty"`
	LastSeenAtUnixMS     int64  `json:"last_seen_at_unix_ms"`
	LastDirtyAtUnixMS    int64  `json:"last_dirty_at_unix_ms"`
	LastVerifiedAtUnixMS int64  `json:"last_verified_at_unix_ms"`
	CooldownUntilUnixMS  int64  `json:"cooldown_until_unix_ms"`
	DirtyReason          string `json:"dirty_reason,omitempty"`
	RefreshState         string `json:"refresh_state,omitempty"`
	UpdatedAtUnixMS      int64  `json:"updated_at_unix_ms"`
}

type DomainPoolVariant struct {
	PoolTag              string `json:"pool_tag"`
	Domain               string `json:"domain"`
	VariantKey           string `json:"variant_key"`
	TotalCount           int    `json:"total_count"`
	Score                int    `json:"score"`
	QTypeMask            uint8  `json:"qtype_mask"`
	FlagsMask            uint8  `json:"flags_mask"`
	Promoted             bool   `json:"promoted"`
	LastSource           string `json:"last_source,omitempty"`
	LastSeenAtUnixMS     int64  `json:"last_seen_at_unix_ms"`
	LastDirtyAtUnixMS    int64  `json:"last_dirty_at_unix_ms"`
	LastVerifiedAtUnixMS int64  `json:"last_verified_at_unix_ms"`
	CooldownUntilUnixMS  int64  `json:"cooldown_until_unix_ms"`
	DirtyReason          string `json:"dirty_reason,omitempty"`
	RefreshState         string `json:"refresh_state,omitempty"`
	ConflictCount        int    `json:"conflict_count"`
	UpdatedAtUnixMS      int64  `json:"updated_at_unix_ms"`
}

type DomainPoolState struct {
	Meta     DomainPoolMeta      `json:"meta"`
	Domains  []DomainPoolDomain  `json:"domains"`
	Variants []DomainPoolVariant `json:"variants"`
}

func SaveDomainPoolStateToPath(path string, state DomainPoolState) error {
	if err := validateDomainPoolState(state); err != nil {
		return err
	}
	store, err := getRuntimeStateStoreByPath(path)
	if err != nil {
		return err
	}
	tx, err := store.db.DB().Begin()
	if err != nil {
		return fmt.Errorf("begin domain pool tx: %w", err)
	}
	if err := replaceDomainPoolState(tx, state); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit domain pool %s: %w", state.Meta.PoolTag, err)
	}
	return nil
}

func LoadDomainPoolStateFromPath(path, poolTag string) (DomainPoolState, bool, error) {
	store, err := getRuntimeStateStoreByPath(path)
	if err != nil {
		return DomainPoolState{}, false, err
	}
	meta, ok, err := loadDomainPoolMeta(store.db.DB(), poolTag)
	if err != nil || !ok {
		return DomainPoolState{}, ok, err
	}
	domains, err := loadDomainPoolDomains(store.db.DB(), poolTag)
	if err != nil {
		return DomainPoolState{}, false, err
	}
	variants, err := loadDomainPoolVariants(store.db.DB(), poolTag)
	if err != nil {
		return DomainPoolState{}, false, err
	}
	return DomainPoolState{Meta: meta, Domains: domains, Variants: variants}, true, nil
}

func ListDomainPoolMetasFromPath(path string) ([]DomainPoolMeta, error) {
	store, err := getRuntimeStateStoreByPath(path)
	if err != nil {
		return nil, err
	}
	rows, err := store.db.DB().Query(`
		SELECT pool_tag, pool_kind, memory_id, policy_json, domain_count, variant_count,
		       dirty_domain_count, promoted_domain_count, published_domain_count,
		       total_observations, dropped_observations, dropped_by_buffer, dropped_by_cap,
		       evicted_domains, evicted_variants, last_ingested_at_unix_ms,
		       last_flush_at_unix_ms, last_publish_at_unix_ms, last_prune_at_unix_ms,
		       updated_at_unix_ms
		FROM domain_pool_meta
		ORDER BY pool_tag ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("list domain_pool_meta: %w", err)
	}
	defer rows.Close()

	items := make([]DomainPoolMeta, 0)
	for rows.Next() {
		item, err := scanDomainPoolMeta(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func validateDomainPoolState(state DomainPoolState) error {
	if strings.TrimSpace(state.Meta.PoolTag) == "" {
		return fmt.Errorf("domain pool meta.pool_tag is empty")
	}
	if err := validateDomainPoolPolicy(state.Meta.PoolTag, &state.Meta.Policy); err != nil {
		return err
	}
	for _, domain := range state.Domains {
		if err := validateDomainPoolDomain(state.Meta.PoolTag, domain); err != nil {
			return err
		}
	}
	for _, variant := range state.Variants {
		if err := validateDomainPoolVariant(state.Meta.PoolTag, variant); err != nil {
			return err
		}
	}
	return nil
}

func validateDomainPoolDomain(poolTag string, item DomainPoolDomain) error {
	if item.PoolTag != poolTag {
		return fmt.Errorf("domain pool domain %s has mismatched pool_tag %q", item.Domain, item.PoolTag)
	}
	if strings.TrimSpace(item.Domain) == "" {
		return fmt.Errorf("domain pool domain is empty")
	}
	return nil
}

func validateDomainPoolVariant(poolTag string, item DomainPoolVariant) error {
	if item.PoolTag != poolTag {
		return fmt.Errorf("domain pool variant %s has mismatched pool_tag %q", item.VariantKey, item.PoolTag)
	}
	if strings.TrimSpace(item.Domain) == "" {
		return fmt.Errorf("domain pool variant domain is empty")
	}
	if strings.TrimSpace(item.VariantKey) == "" {
		return fmt.Errorf("domain pool variant key is empty")
	}
	return nil
}

func replaceDomainPoolState(tx *sql.Tx, state DomainPoolState) error {
	if err := saveDomainPoolMeta(tx, state.Meta); err != nil {
		return err
	}
	if err := clearDomainPoolRows(tx, state.Meta.PoolTag); err != nil {
		return err
	}
	if err := saveDomainPoolDomains(tx, state.Domains); err != nil {
		return err
	}
	return saveDomainPoolVariants(tx, state.Variants)
}

func saveDomainPoolMeta(tx *sql.Tx, meta DomainPoolMeta) error {
	policyJSON, err := json.Marshal(meta.Policy)
	if err != nil {
		return fmt.Errorf("marshal domain pool policy %s: %w", meta.PoolTag, err)
	}
	_, err = tx.Exec(`
		INSERT INTO domain_pool_meta (
			pool_tag, pool_kind, memory_id, policy_json, domain_count, variant_count,
			dirty_domain_count, promoted_domain_count, published_domain_count,
			total_observations, dropped_observations, dropped_by_buffer, dropped_by_cap,
			evicted_domains, evicted_variants, last_ingested_at_unix_ms,
			last_flush_at_unix_ms, last_publish_at_unix_ms, last_prune_at_unix_ms,
			updated_at_unix_ms
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, unixepoch('subsec') * 1000)
		ON CONFLICT(pool_tag) DO UPDATE SET
			pool_kind = excluded.pool_kind,
			memory_id = excluded.memory_id,
			policy_json = excluded.policy_json,
			domain_count = excluded.domain_count,
			variant_count = excluded.variant_count,
			dirty_domain_count = excluded.dirty_domain_count,
			promoted_domain_count = excluded.promoted_domain_count,
			published_domain_count = excluded.published_domain_count,
			total_observations = excluded.total_observations,
			dropped_observations = excluded.dropped_observations,
			dropped_by_buffer = excluded.dropped_by_buffer,
			dropped_by_cap = excluded.dropped_by_cap,
			evicted_domains = excluded.evicted_domains,
			evicted_variants = excluded.evicted_variants,
			last_ingested_at_unix_ms = excluded.last_ingested_at_unix_ms,
			last_flush_at_unix_ms = excluded.last_flush_at_unix_ms,
			last_publish_at_unix_ms = excluded.last_publish_at_unix_ms,
			last_prune_at_unix_ms = excluded.last_prune_at_unix_ms,
			updated_at_unix_ms = excluded.updated_at_unix_ms
	`, meta.PoolTag, meta.PoolKind, meta.MemoryID, string(policyJSON), meta.DomainCount, meta.VariantCount,
		meta.DirtyDomainCount, meta.PromotedDomainCount, meta.PublishedDomainCount,
		meta.TotalObservations, meta.DroppedObservations, meta.DroppedByBuffer, meta.DroppedByCap,
		meta.EvictedDomains, meta.EvictedVariants, meta.LastIngestedAtUnixMS,
		meta.LastFlushAtUnixMS, meta.LastPublishAtUnixMS, meta.LastPruneAtUnixMS)
	if err != nil {
		return fmt.Errorf("save domain_pool_meta %s: %w", meta.PoolTag, err)
	}
	return nil
}

func clearDomainPoolRows(tx *sql.Tx, poolTag string) error {
	if _, err := tx.Exec(`DELETE FROM domain_pool_variant WHERE pool_tag = ?`, poolTag); err != nil {
		return fmt.Errorf("clear domain_pool_variant %s: %w", poolTag, err)
	}
	if _, err := tx.Exec(`DELETE FROM domain_pool_domain WHERE pool_tag = ?`, poolTag); err != nil {
		return fmt.Errorf("clear domain_pool_domain %s: %w", poolTag, err)
	}
	return nil
}

func saveDomainPoolDomains(tx *sql.Tx, items []DomainPoolDomain) error {
	for _, item := range items {
		if _, err := tx.Exec(`
			INSERT INTO domain_pool_domain (
				pool_tag, domain, total_count, score, qtype_mask, flags_mask,
				variant_count, dirty_variant_count, promoted, last_source,
				last_seen_at_unix_ms, last_dirty_at_unix_ms, last_verified_at_unix_ms,
				cooldown_until_unix_ms, dirty_reason, refresh_state, updated_at_unix_ms
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, unixepoch('subsec') * 1000)
		`, item.PoolTag, item.Domain, item.TotalCount, item.Score, item.QTypeMask, item.FlagsMask,
			item.VariantCount, item.DirtyVariantCount, boolToInt(item.Promoted), item.LastSource,
			item.LastSeenAtUnixMS, item.LastDirtyAtUnixMS, item.LastVerifiedAtUnixMS,
			item.CooldownUntilUnixMS, item.DirtyReason, item.RefreshState); err != nil {
			return fmt.Errorf("insert domain_pool_domain %s/%s: %w", item.PoolTag, item.Domain, err)
		}
	}
	return nil
}

func saveDomainPoolVariants(tx *sql.Tx, items []DomainPoolVariant) error {
	for _, item := range items {
		if _, err := tx.Exec(`
			INSERT INTO domain_pool_variant (
				pool_tag, domain, variant_key, total_count, score, qtype_mask,
				flags_mask, promoted, last_source, last_seen_at_unix_ms,
				last_dirty_at_unix_ms, last_verified_at_unix_ms, cooldown_until_unix_ms,
				dirty_reason, refresh_state, conflict_count, updated_at_unix_ms
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, unixepoch('subsec') * 1000)
		`, item.PoolTag, item.Domain, item.VariantKey, item.TotalCount, item.Score, item.QTypeMask,
			item.FlagsMask, boolToInt(item.Promoted), item.LastSource, item.LastSeenAtUnixMS,
			item.LastDirtyAtUnixMS, item.LastVerifiedAtUnixMS, item.CooldownUntilUnixMS,
			item.DirtyReason, item.RefreshState, item.ConflictCount); err != nil {
			return fmt.Errorf("insert domain_pool_variant %s/%s/%s: %w", item.PoolTag, item.Domain, item.VariantKey, err)
		}
	}
	return nil
}

func loadDomainPoolMeta(db *sql.DB, poolTag string) (DomainPoolMeta, bool, error) {
	row := db.QueryRow(`
		SELECT pool_tag, pool_kind, memory_id, policy_json, domain_count, variant_count,
		       dirty_domain_count, promoted_domain_count, published_domain_count,
		       total_observations, dropped_observations, dropped_by_buffer, dropped_by_cap,
		       evicted_domains, evicted_variants, last_ingested_at_unix_ms,
		       last_flush_at_unix_ms, last_publish_at_unix_ms, last_prune_at_unix_ms,
		       updated_at_unix_ms
		FROM domain_pool_meta
		WHERE pool_tag = ?
	`, poolTag)
	meta, err := scanDomainPoolMeta(row)
	if err == sql.ErrNoRows {
		return DomainPoolMeta{}, false, nil
	}
	if err != nil {
		return DomainPoolMeta{}, false, err
	}
	return meta, true, nil
}

func loadDomainPoolDomains(db *sql.DB, poolTag string) ([]DomainPoolDomain, error) {
	rows, err := db.Query(`
		SELECT pool_tag, domain, total_count, score, qtype_mask, flags_mask,
		       variant_count, dirty_variant_count, promoted, last_source,
		       last_seen_at_unix_ms, last_dirty_at_unix_ms, last_verified_at_unix_ms,
		       cooldown_until_unix_ms, dirty_reason, refresh_state, updated_at_unix_ms
		FROM domain_pool_domain
		WHERE pool_tag = ?
		ORDER BY score DESC, total_count DESC, domain ASC
	`, poolTag)
	if err != nil {
		return nil, fmt.Errorf("query domain_pool_domain %s: %w", poolTag, err)
	}
	defer rows.Close()
	return scanDomainPoolDomains(rows)
}

func loadDomainPoolVariants(db *sql.DB, poolTag string) ([]DomainPoolVariant, error) {
	rows, err := db.Query(`
		SELECT pool_tag, domain, variant_key, total_count, score, qtype_mask,
		       flags_mask, promoted, last_source, last_seen_at_unix_ms,
		       last_dirty_at_unix_ms, last_verified_at_unix_ms, cooldown_until_unix_ms,
		       dirty_reason, refresh_state, conflict_count, updated_at_unix_ms
		FROM domain_pool_variant
		WHERE pool_tag = ?
		ORDER BY score DESC, total_count DESC, domain ASC, variant_key ASC
	`, poolTag)
	if err != nil {
		return nil, fmt.Errorf("query domain_pool_variant %s: %w", poolTag, err)
	}
	defer rows.Close()
	return scanDomainPoolVariants(rows)
}

func scanDomainPoolMeta(scanner interface{ Scan(dest ...any) error }) (DomainPoolMeta, error) {
	var item DomainPoolMeta
	var policyJSON string
	if err := scanner.Scan(
		&item.PoolTag, &item.PoolKind, &item.MemoryID, &policyJSON, &item.DomainCount, &item.VariantCount,
		&item.DirtyDomainCount, &item.PromotedDomainCount, &item.PublishedDomainCount,
		&item.TotalObservations, &item.DroppedObservations, &item.DroppedByBuffer, &item.DroppedByCap,
		&item.EvictedDomains, &item.EvictedVariants, &item.LastIngestedAtUnixMS,
		&item.LastFlushAtUnixMS, &item.LastPublishAtUnixMS, &item.LastPruneAtUnixMS, &item.UpdatedAtUnixMS,
	); err != nil {
		return DomainPoolMeta{}, err
	}
	if err := json.Unmarshal([]byte(policyJSON), &item.Policy); err != nil {
		return DomainPoolMeta{}, fmt.Errorf("decode domain_pool_meta policy %s: %w", item.PoolTag, err)
	}
	return item, nil
}

func scanDomainPoolDomains(rows *sql.Rows) ([]DomainPoolDomain, error) {
	items := make([]DomainPoolDomain, 0)
	for rows.Next() {
		var item DomainPoolDomain
		var promoted int
		if err := rows.Scan(
			&item.PoolTag, &item.Domain, &item.TotalCount, &item.Score, &item.QTypeMask, &item.FlagsMask,
			&item.VariantCount, &item.DirtyVariantCount, &promoted, &item.LastSource,
			&item.LastSeenAtUnixMS, &item.LastDirtyAtUnixMS, &item.LastVerifiedAtUnixMS,
			&item.CooldownUntilUnixMS, &item.DirtyReason, &item.RefreshState, &item.UpdatedAtUnixMS,
		); err != nil {
			return nil, fmt.Errorf("scan domain_pool_domain: %w", err)
		}
		item.Promoted = promoted == 1
		items = append(items, item)
	}
	return items, rows.Err()
}

func scanDomainPoolVariants(rows *sql.Rows) ([]DomainPoolVariant, error) {
	items := make([]DomainPoolVariant, 0)
	for rows.Next() {
		var item DomainPoolVariant
		var promoted int
		if err := rows.Scan(
			&item.PoolTag, &item.Domain, &item.VariantKey, &item.TotalCount, &item.Score, &item.QTypeMask,
			&item.FlagsMask, &promoted, &item.LastSource, &item.LastSeenAtUnixMS,
			&item.LastDirtyAtUnixMS, &item.LastVerifiedAtUnixMS, &item.CooldownUntilUnixMS,
			&item.DirtyReason, &item.RefreshState, &item.ConflictCount, &item.UpdatedAtUnixMS,
		); err != nil {
			return nil, fmt.Errorf("scan domain_pool_variant: %w", err)
		}
		item.Promoted = promoted == 1
		items = append(items, item)
	}
	return items, rows.Err()
}
