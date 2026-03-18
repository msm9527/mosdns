(function (global) {
    const DAY_MS = 24 * 60 * 60 * 1000;
    const ALL_FIELDS = [
        'query_name',
        'client_ip',
        'trace_id',
        'domain_set',
        'answer',
        'query_type',
        'query_class',
        'response_code',
        'upstream_tag',
        'transport',
        'server_name',
        'url_path',
        'cache_status'
    ];

    function pad(value) {
        return String(value).padStart(2, '0');
    }

    function toLocalInputValue(date) {
        return [
            date.getFullYear(),
            '-',
            pad(date.getMonth() + 1),
            '-',
            pad(date.getDate()),
            'T',
            pad(date.getHours()),
            ':',
            pad(date.getMinutes())
        ].join('');
    }

    function defaultCriteria() {
        const now = new Date();
        const from = new Date(now.getTime() - DAY_MS);
        return {
            keyword: '',
            mode: 'fuzzy',
            fields: [...ALL_FIELDS],
            from: toLocalInputValue(from),
            to: toLocalInputValue(now),
            filters: {
                domain: '',
                domainMode: 'exact',
                clientIP: '',
                responseCode: '',
                queryType: '',
                domainSet: '',
                upstreamTag: '',
                upstreamMode: 'exact',
                transport: '',
                answer: '',
                answerMode: 'exact',
                hasAnswer: 'any',
                durationMin: '',
                durationMax: ''
            }
        };
    }

    function normalizeFields(fields) {
        const unique = Array.from(new Set((fields || []).filter(field => ALL_FIELDS.includes(field))));
        return unique.length > 0 ? unique : [...ALL_FIELDS];
    }

    function normalizeDateInput(value, fallback) {
        if (!value) return fallback;
        const parsed = new Date(value);
        if (Number.isNaN(parsed.getTime())) return fallback;
        return toLocalInputValue(parsed);
    }

    function normalizeMode(value, fallback) {
        return value === 'exact' || value === 'fuzzy' ? value : fallback;
    }

    function normalizeCriteria(raw) {
        const base = defaultCriteria();
        const filters = { ...base.filters, ...(raw?.filters || {}) };
        return {
            keyword: String(raw?.keyword || '').trim(),
            mode: normalizeMode(raw?.mode, base.mode),
            fields: normalizeFields(raw?.fields),
            from: normalizeDateInput(raw?.from, base.from),
            to: normalizeDateInput(raw?.to, base.to),
            filters: {
                domain: String(filters.domain || '').trim(),
                domainMode: normalizeMode(filters.domainMode, base.filters.domainMode),
                clientIP: String(filters.clientIP || '').trim(),
                responseCode: String(filters.responseCode || '').trim().toUpperCase(),
                queryType: String(filters.queryType || '').trim().toUpperCase(),
                domainSet: String(filters.domainSet || '').trim(),
                upstreamTag: String(filters.upstreamTag || '').trim(),
                upstreamMode: normalizeMode(filters.upstreamMode, base.filters.upstreamMode),
                transport: String(filters.transport || '').trim(),
                answer: String(filters.answer || '').trim(),
                answerMode: normalizeMode(filters.answerMode, base.filters.answerMode),
                hasAnswer: ['any', 'yes', 'no'].includes(filters.hasAnswer) ? filters.hasAnswer : base.filters.hasAnswer,
                durationMin: String(filters.durationMin || '').trim(),
                durationMax: String(filters.durationMax || '').trim()
            }
        };
    }

    function toISO(value) {
        return new Date(value).toISOString();
    }

    function maybeNumber(value) {
        if (value === '') return null;
        const parsed = Number(value);
        return Number.isFinite(parsed) ? parsed : null;
    }

    function buildPayload(criteria, limit, cursor) {
        const normalized = normalizeCriteria(criteria);
        const payload = {
            time_range: {
                from: toISO(normalized.from),
                to: toISO(normalized.to)
            },
            page: {
                limit,
                cursor: cursor || ''
            },
            filters: {}
        };
        if (normalized.keyword) {
            payload.keyword = {
                value: normalized.keyword,
                mode: normalized.mode,
                fields: normalized.fields
            };
        }
        if (normalized.filters.domain) {
            payload.filters.domain = {
                value: normalized.filters.domain,
                mode: normalized.filters.domainMode
            };
        }
        if (normalized.filters.clientIP) {
            payload.filters.client_ip = {
                value: normalized.filters.clientIP,
                mode: 'exact'
            };
        }
        if (normalized.filters.responseCode) payload.filters.response_code = normalized.filters.responseCode;
        if (normalized.filters.queryType) payload.filters.query_type = normalized.filters.queryType;
        if (normalized.filters.domainSet) {
            payload.filters.domain_set = {
                value: normalized.filters.domainSet,
                mode: 'exact'
            };
        }
        if (normalized.filters.upstreamTag) {
            payload.filters.upstream_tag = {
                value: normalized.filters.upstreamTag,
                mode: normalized.filters.upstreamMode
            };
        }
        if (normalized.filters.transport) payload.filters.transport = normalized.filters.transport;
        if (normalized.filters.answer) {
            payload.filters.answer = {
                value: normalized.filters.answer,
                mode: normalized.filters.answerMode
            };
        }
        if (normalized.filters.hasAnswer === 'yes') payload.filters.has_answer = true;
        if (normalized.filters.hasAnswer === 'no') payload.filters.has_answer = false;
        const durationMin = maybeNumber(normalized.filters.durationMin);
        const durationMax = maybeNumber(normalized.filters.durationMax);
        if (durationMin !== null) payload.filters.duration_ms_min = durationMin;
        if (durationMax !== null) payload.filters.duration_ms_max = durationMax;
        return payload;
    }

    function hasActiveCriteria(criteria) {
        const normalized = normalizeCriteria(criteria);
        if (normalized.keyword) return true;
        return Object.values(normalized.filters).some(value => value !== '' && value !== 'any');
    }

    function formatRange(criteria) {
        const normalized = normalizeCriteria(criteria);
        const from = new Date(normalized.from).toLocaleString('zh-CN', { hour12: false });
        const to = new Date(normalized.to).toLocaleString('zh-CN', { hour12: false });
        return `${from} ~ ${to}`;
    }

    global.mosdnsAuditSearch = {
        ALL_FIELDS,
        defaultCriteria,
        normalizeCriteria,
        buildPayload,
        hasActiveCriteria,
        formatRange
    };
})(window);
