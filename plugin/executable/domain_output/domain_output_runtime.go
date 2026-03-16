package domain_output

import (
	"bufio"
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"go.uber.org/zap"
)

func (d *domainOutput) loadFromDataset() error {
	if d.statDatasetKey == "" {
		return nil
	}
	dataset, ok, err := coremain.LoadGeneratedDatasetFromPath(d.dbPath, d.statDatasetKey)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}

	scanner := bufio.NewScanner(strings.NewReader(dataset.Content))
	today := time.Now().Format("2006-01-02")
	d.mu.Lock()
	defer d.mu.Unlock()

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		count, _ := strconv.Atoi(fields[0])
		lastDate := today
		domain := ""
		startExtras := 2
		if len(fields) >= 3 && strings.Count(fields[1], "-") == 2 {
			lastDate = fields[1]
			domain = fields[2]
			startExtras = 3
		} else {
			domain = fields[1]
		}

		entry := &statEntry{Count: count, Score: count, LastDate: lastDate}
		for _, field := range fields[startExtras:] {
			k, v, ok := strings.Cut(field, "=")
			if !ok {
				continue
			}
			switch k {
			case "qmask":
				if parsed, err := strconv.Atoi(v); err == nil {
					entry.QTypeMask = uint8(parsed)
				}
			case "score":
				if parsed, err := strconv.Atoi(v); err == nil {
					entry.Score = parsed
				}
			case "promoted":
				entry.Promoted = v == "1"
			case "last_seen":
				entry.LastSeenAt = restoreStatToken(v)
			case "last_dirty":
				entry.LastDirtyAt = restoreStatToken(v)
			case "last_verified":
				entry.LastVerifiedAt = restoreStatToken(v)
			case "dirty_reason":
				entry.DirtyReason = restoreStatToken(v)
			case "refresh_state":
				entry.RefreshState = restoreStatToken(v)
			case "cooldown_until":
				entry.CooldownUntil = restoreStatToken(v)
			case "conflicts":
				if parsed, err := strconv.Atoi(v); err == nil {
					entry.ConflictCount = parsed
				}
			}
		}
		entry.Promoted = d.shouldPromote(entry)
		if d.maxEntries > 0 && len(d.stats) >= d.maxEntries {
			continue
		}
		d.stats[domain] = entry
		atomic.AddInt64(&d.totalCount, int64(count))
	}
	return scanner.Err()
}

func (d *domainOutput) saveGeneratedDatasets(snapshot writeSnapshot) error {
	if err := d.saveDataset(
		d.statDatasetKey,
		coremain.GeneratedDatasetFormatDomainOutputStat,
		d.renderStatContent(snapshot),
	); err != nil {
		return err
	}
	if d.ruleDatasetKey == "" {
		return nil
	}
	return d.saveDataset(
		d.ruleDatasetKey,
		coremain.GeneratedDatasetFormatDomainOutputRule,
		d.renderRuleContent(snapshot),
	)
}

func (d *domainOutput) saveDataset(key, format, body string) error {
	if strings.TrimSpace(key) == "" {
		return nil
	}
	return coremain.SaveGeneratedDatasetEntryToPath(d.dbPath, key, format, body, "")
}

func (d *domainOutput) renderStatContent(snapshot writeSnapshot) string {
	var b strings.Builder
	for _, item := range snapshot.items {
		_, _ = fmt.Fprintf(
			&b,
			"%010d %s %s qmask=%d score=%d promoted=%d last_seen=%s last_dirty=%s last_verified=%s dirty_reason=%s refresh_state=%s cooldown_until=%s conflicts=%d\n",
			item.Count,
			item.Date,
			item.Domain,
			item.QMask,
			item.Score,
			boolToInt(item.Prom),
			sanitizeStatToken(item.LastSeenAt),
			sanitizeStatToken(item.LastDirtyAt),
			sanitizeStatToken(item.LastVerifiedAt),
			sanitizeStatToken(item.DirtyReason),
			sanitizeStatToken(item.RefreshState),
			sanitizeStatToken(item.CooldownUntil),
			item.ConflictCount,
		)
	}
	return b.String()
}

func (d *domainOutput) renderRuleContent(snapshot writeSnapshot) string {
	var b strings.Builder
	for _, domain := range snapshot.rules {
		_, _ = fmt.Fprintf(&b, "full:%s\n", domain)
	}
	return b.String()
}

func (d *domainOutput) publishRules(rules []string) error {
	if d.publishTo == "" {
		return nil
	}
	if d.manager == nil {
		return fmt.Errorf("manager is nil")
	}
	controller, ok := d.manager.GetPlugin(d.publishTo).(coremain.ListContentController)
	if !ok || controller == nil {
		return fmt.Errorf("%s is not a list controller", d.publishTo)
	}

	values := make([]string, 0, len(rules))
	for _, domain := range rules {
		values = append(values, "full:"+domain)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := controller.ReplaceListRuntime(ctx, values)
	return err
}

func (d *domainOutput) notifyDirty(job coremain.DomainRefreshJob) {
	if d.policy.requeryTag == "" || d.manager == nil {
		return
	}
	enqueuer, ok := d.manager.GetPlugin(d.policy.requeryTag).(coremain.DomainRefreshJobEnqueuer)
	if !ok || enqueuer == nil {
		if d.logger != nil {
			d.logger.Warn(
				"domain_output requery plugin not found",
				zap.String("plugin", d.pluginTag),
				zap.String("requery_tag", d.policy.requeryTag),
			)
		}
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if !enqueuer.EnqueueDomainRefresh(ctx, job) && d.logger != nil {
		d.logger.Warn(
			"domain_output requery enqueue skipped",
			zap.String("plugin", d.pluginTag),
			zap.String("requery_tag", d.policy.requeryTag),
			zap.String("domain", job.Domain),
		)
	}
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func sanitizeStatToken(v string) string {
	if v == "" {
		return "-"
	}
	return strings.ReplaceAll(v, " ", "_")
}

func restoreStatToken(v string) string {
	if v == "-" {
		return ""
	}
	return strings.ReplaceAll(v, "_", " ")
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
