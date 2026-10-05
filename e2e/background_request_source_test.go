package e2e_test

import (
	"net/http"
	"net/url"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/IrineSistiana/mosdns/v5/coremain"
)

const sourceE2EPoolTag = "my_realiplist"

type sourceE2EQueueStatus struct {
	Pending   int   `json:"pending_queue"`
	Triggered int64 `json:"on_demand_triggered"`
	Skipped   int64 `json:"on_demand_skipped"`
}

// Reuse TestServiceE2E's fixture because switch registrations live for the process.
// Each control owns a synthetic domain and leaves the common promotion threshold unchanged.
func (fx *serviceE2EFixture) testBackgroundRequestProvenance(t *testing.T, rec *e2eCaseRecorder) {
	var switches []serviceE2ESwitchState
	fx.getJSON(t, "/api/v1/control/switches/", &switches)
	changes := map[string]string{"core_mode": "compat", "cn_answer_mode": "realip", "main_cache": "off", "branch_cache": "off", "udp_fast_path": "on"}
	t.Cleanup(func() {
		for _, state := range switches {
			if _, changed := changes[state.Name]; changed {
				fx.setSwitch(t, state.Name, state.Value)
			}
		}
	})
	for name, value := range changes {
		fx.setSwitch(t, name, value)
	}

	for _, control := range []struct{ source, network, addr string }{
		{"prewarm", "udp", fx.requeryAddr},
		{"prewarm", "tcp", fx.requeryAddr},
		{"refresh", "udp", fx.refreshAddr},
		{"refresh", "tcp", fx.refreshAddr},
	} {
		t.Run(control.source+"_"+control.network, func(t *testing.T) {
			domain := "source-" + control.source + "-" + control.network + ".example.test"
			beforePool := fx.sourcePoolStats(t, sourceE2EPoolTag)
			beforeTop := fx.sourcePoolStats(t, "top_domains")
			beforeQueue := fx.sourceQueueStatus(t)
			for repeat := 0; repeat < 2; repeat++ {
				resp := fx.mustExchange(t, control.network, control.addr, domain, 1)
				requireServiceE2EARecord(t, resp, "1.1.1.1")
				entry := fx.waitSourceEntry(t, domain, 0, true)
				if entry.Score != 0 || entry.QTypeMask != 1 {
					t.Fatalf("background activity changed demand or qtype fields %+v", entry)
				}
				state := fx.savedSourcePool(t)
				stored, variant := sourceStoredDomain(t, state, domain)
				if stored.TotalCount != 0 || stored.Score != 0 || stored.LastSeenAtUnixMS != 0 || !stored.Promoted || stored.LastVerifiedAtUnixMS <= 0 || stored.QTypeMask != 1 || stored.LastSource != control.source || stored.RefreshState != "clean" || stored.DirtyReason != "" || stored.CooldownUntilUnixMS != 0 {
					t.Fatalf("background classification contract lost %+v", stored)
				}
				if variant.TotalCount != 0 || variant.Score != 0 || variant.LastSeenAtUnixMS != 0 || !variant.Promoted || variant.LastVerifiedAtUnixMS != stored.LastVerifiedAtUnixMS || variant.QTypeMask != 1 || variant.LastSource != control.source || variant.RefreshState != "clean" || variant.DirtyReason != "" || variant.CooldownUntilUnixMS != 0 {
					t.Fatalf("background variant contract lost %+v", variant)
				}
				reloaded := fx.savedSourcePool(t)
				readDomain, readVariant := sourceStoredDomain(t, reloaded, domain)
				if !reflect.DeepEqual(stored, readDomain) || !reflect.DeepEqual(variant, readVariant) {
					t.Fatalf("business fields changed across persistence reads before=%+v/%+v after=%+v/%+v", stored, variant, readDomain, readVariant)
				}
				fx.requireSourceCounters(t, beforePool, beforeTop, beforeQueue, 0)
			}
			rec.AddCheck(control.source+" "+control.network, "two real listener requests retained Count=0, Score=0, LastSeen=0, verified promoted classification, clean state and unchanged demand queue")
		})
	}

	t.Run("user", func(t *testing.T) {
		domain := "source-user-control.example.test"
		beforePool := fx.sourcePoolStats(t, sourceE2EPoolTag)
		beforeTop := fx.sourcePoolStats(t, "top_domains")
		beforeQueue := fx.sourceQueueStatus(t)
		for i, network := range []string{"udp", "tcp"} {
			resp := fx.mustExchange(t, network, fx.dnsAddr, domain, 1)
			requireServiceE2EARecord(t, resp, "1.1.1.1")
			entry := fx.waitSourceEntry(t, domain, i+1, false)
			if entry.Score != i+1 || entry.QTypeMask != 1 {
				t.Fatalf("user activity fields not updated %+v", entry)
			}
			stored, variant := sourceStoredDomain(t, fx.savedSourcePool(t), domain)
			if stored.TotalCount != i+1 || stored.Score != i+1 || stored.LastSeenAtUnixMS <= 0 || stored.LastSource != "live" || variant.TotalCount != i+1 || variant.LastSeenAtUnixMS <= 0 {
				t.Fatalf("user state not persisted %+v/%+v", stored, variant)
			}
			fx.requireSourceCounters(t, beforePool, beforeTop, beforeQueue, int64(i+1))
		}
		rec.AddCheck("user udp and tcp", "main listener requests increased pool observations, top observations and persisted Count/Score from one to two under the existing promotion threshold")
	})
	rec.SetDetail("real UDP/TCP prewarm and refresh listeners establish verified routing memory without recording user demand or scheduling on-demand work; the main listener still records user activity")
}

func (fx *serviceE2EFixture) sourcePoolStats(t *testing.T, tag string) coremain.DomainStatsSnapshot {
	t.Helper()
	var stats coremain.DomainStatsSnapshot
	fx.getJSON(t, "/api/v1/memory/"+tag+"/stats", &stats)
	return stats
}

func (fx *serviceE2EFixture) sourceQueueStatus(t *testing.T) sourceE2EQueueStatus {
	t.Helper()
	var status sourceE2EQueueStatus
	fx.getJSON(t, "/api/v1/control/requery/status", &status)
	return status
}

func (fx *serviceE2EFixture) waitSourceEntry(t *testing.T, domain string, count int, promoted bool) coremain.MemoryEntry {
	t.Helper()
	var last coremain.MemoryEntriesResponse
	var lastErr error
	path := "/api/v1/memory/" + sourceE2EPoolTag + "/entries?q=" + url.QueryEscape(domain)
	if err := waitServiceE2EEventually(3*time.Second, func() bool {
		lastErr = fx.doJSON(http.MethodGet, path, nil, &last, http.StatusOK)
		return lastErr == nil && len(last.Items) == 1 && last.Items[0].Domain == domain && last.Items[0].Count == count && last.Items[0].Promoted == promoted
	}, "classification worker did not reach expected API state"); err != nil {
		t.Fatalf("%v domain=%s count=%d promoted=%t last=%+v error=%v", err, domain, count, promoted, last, lastErr)
	}
	return last.Items[0]
}

func (fx *serviceE2EFixture) savedSourcePool(t *testing.T) coremain.DomainPoolState {
	t.Helper()
	fx.postJSON(t, "/api/v1/memory/"+sourceE2EPoolTag+"/save", nil, nil, http.StatusOK)
	state, found, err := coremain.LoadDomainPoolStateFromPath(filepath.Join(fx.configDir, "db", "control.db"), sourceE2EPoolTag)
	if err != nil || !found {
		t.Fatalf("read saved pool found=%v error=%v", found, err)
	}
	return state
}

func sourceStoredDomain(t *testing.T, state coremain.DomainPoolState, domain string) (coremain.DomainPoolDomain, coremain.DomainPoolVariant) {
	t.Helper()
	var stored coremain.DomainPoolDomain
	var variant coremain.DomainPoolVariant
	domains, variants := 0, 0
	for _, item := range state.Domains {
		if item.Domain == domain {
			stored = item
			domains++
		}
	}
	for _, item := range state.Variants {
		if item.Domain == domain {
			variant = item
			variants++
		}
	}
	if domains != 1 || variants != 1 {
		t.Fatalf("expected one stored domain and variant for %s, got %d/%d", domain, domains, variants)
	}
	return stored, variant
}

func (fx *serviceE2EFixture) requireSourceCounters(t *testing.T, beforePool, beforeTop coremain.DomainStatsSnapshot, beforeQueue sourceE2EQueueStatus, demandDelta int64) {
	t.Helper()
	var lastPool, lastTop coremain.DomainStatsSnapshot
	var lastQueue sourceE2EQueueStatus
	if err := waitServiceE2EEventually(time.Second, func() bool {
		lastPool = fx.sourcePoolStats(t, sourceE2EPoolTag)
		lastTop = fx.sourcePoolStats(t, "top_domains")
		lastQueue = fx.sourceQueueStatus(t)
		return lastPool.TotalObservations == beforePool.TotalObservations+demandDelta && lastTop.TotalObservations == beforeTop.TotalObservations+demandDelta && lastQueue == beforeQueue
	}, "request source counters or queue changed unexpectedly"); err != nil {
		t.Fatalf("%v pool=%d/%d top=%d/%d queue=%+v/%+v", err, beforePool.TotalObservations+demandDelta, lastPool.TotalObservations, beforeTop.TotalObservations+demandDelta, lastTop.TotalObservations, beforeQueue, lastQueue)
	}
	// Notifications run asynchronously after the pool worker. Check a bounded
	// quiet window so an immediate API snapshot cannot hide a late enqueue.
	deadline := time.Now().Add(100 * time.Millisecond)
	for time.Now().Before(deadline) {
		lastPool = fx.sourcePoolStats(t, sourceE2EPoolTag)
		lastTop = fx.sourcePoolStats(t, "top_domains")
		lastQueue = fx.sourceQueueStatus(t)
		if lastPool.TotalObservations != beforePool.TotalObservations+demandDelta || lastTop.TotalObservations != beforeTop.TotalObservations+demandDelta || lastQueue != beforeQueue {
			t.Fatalf("late demand side effect pool=%+v top=%+v queue=%+v expected_queue=%+v", lastPool, lastTop, lastQueue, beforeQueue)
		}
		time.Sleep(10 * time.Millisecond)
	}

}
