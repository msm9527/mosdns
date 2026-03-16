package coremain

import (
	"database/sql"
	"fmt"
	"strings"
)

type DomainPoolDomainQuery struct {
	PoolTag string
	Query   string
	Offset  int
	Limit   int
}

type DomainPoolVariantQuery struct {
	PoolTag string
	Domain  string
	Query   string
	Offset  int
	Limit   int
}

func ListDomainPoolDomainsFromPath(path string, req DomainPoolDomainQuery) ([]DomainPoolDomain, int, error) {
	store, err := getRuntimeStateStoreByPath(path)
	if err != nil {
		return nil, 0, err
	}
	return listDomainPoolDomains(store.db.DB(), req)
}

func ListDomainPoolVariantsFromPath(path string, req DomainPoolVariantQuery) ([]DomainPoolVariant, int, error) {
	store, err := getRuntimeStateStoreByPath(path)
	if err != nil {
		return nil, 0, err
	}
	return listDomainPoolVariants(store.db.DB(), req)
}

func listDomainPoolDomains(db *sql.DB, req DomainPoolDomainQuery) ([]DomainPoolDomain, int, error) {
	query := normalizeLikeQuery(req.Query)
	offset, limit := normalizePagination(req.Offset, req.Limit)
	total, err := countDomainPoolDomains(db, req.PoolTag, query)
	if err != nil {
		return nil, 0, err
	}
	rows, err := db.Query(`
		SELECT pool_tag, domain, total_count, score, qtype_mask, flags_mask,
		       variant_count, dirty_variant_count, promoted, last_source,
		       last_seen_at_unix_ms, last_dirty_at_unix_ms, last_verified_at_unix_ms,
		       cooldown_until_unix_ms, dirty_reason, refresh_state, updated_at_unix_ms
		FROM domain_pool_domain
		WHERE pool_tag = ? AND domain LIKE ?
		ORDER BY score DESC, total_count DESC, domain ASC
		LIMIT ? OFFSET ?
	`, req.PoolTag, query, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("list domain_pool_domain %s: %w", req.PoolTag, err)
	}
	defer rows.Close()
	items, err := scanDomainPoolDomains(rows)
	return items, total, err
}

func listDomainPoolVariants(db *sql.DB, req DomainPoolVariantQuery) ([]DomainPoolVariant, int, error) {
	query := normalizeLikeQuery(req.Query)
	offset, limit := normalizePagination(req.Offset, req.Limit)
	total, err := countDomainPoolVariants(db, req.PoolTag, req.Domain, query)
	if err != nil {
		return nil, 0, err
	}
	rows, err := db.Query(`
		SELECT pool_tag, domain, variant_key, total_count, score, qtype_mask,
		       flags_mask, promoted, last_source, last_seen_at_unix_ms,
		       last_dirty_at_unix_ms, last_verified_at_unix_ms, cooldown_until_unix_ms,
		       dirty_reason, refresh_state, conflict_count, updated_at_unix_ms
		FROM domain_pool_variant
		WHERE pool_tag = ? AND domain LIKE ? AND variant_key LIKE ?
		ORDER BY score DESC, total_count DESC, domain ASC, variant_key ASC
		LIMIT ? OFFSET ?
	`, req.PoolTag, normalizeLikeQuery(req.Domain), query, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("list domain_pool_variant %s: %w", req.PoolTag, err)
	}
	defer rows.Close()
	items, err := scanDomainPoolVariants(rows)
	return items, total, err
}

func countDomainPoolDomains(db *sql.DB, poolTag, query string) (int, error) {
	var total int
	if err := db.QueryRow(`
		SELECT COUNT(*)
		FROM domain_pool_domain
		WHERE pool_tag = ? AND domain LIKE ?
	`, poolTag, query).Scan(&total); err != nil {
		return 0, fmt.Errorf("count domain_pool_domain %s: %w", poolTag, err)
	}
	return total, nil
}

func countDomainPoolVariants(db *sql.DB, poolTag, domain, query string) (int, error) {
	var total int
	if err := db.QueryRow(`
		SELECT COUNT(*)
		FROM domain_pool_variant
		WHERE pool_tag = ? AND domain LIKE ? AND variant_key LIKE ?
	`, poolTag, normalizeLikeQuery(domain), query).Scan(&total); err != nil {
		return 0, fmt.Errorf("count domain_pool_variant %s: %w", poolTag, err)
	}
	return total, nil
}

func normalizeLikeQuery(query string) string {
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return "%"
	}
	return "%" + trimmed + "%"
}

func normalizePagination(offset, limit int) (int, int) {
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = 100
	}
	return offset, limit
}
