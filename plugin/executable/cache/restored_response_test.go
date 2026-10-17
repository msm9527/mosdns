package cache

import (
	"bytes"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func TestLegacyNegativePersistenceRechecksTTL(t *testing.T) {
	for _, storage := range []string{"dump", "wal"} {
		for _, tc := range []struct {
			name      string
			rcode     int
			age       time.Duration
			zeroSOA   bool
			truncated bool
			signed    bool
			ad        bool
			keep      bool
		}{
			{name: "recent_nodata", age: 5 * time.Second, keep: true},
			{name: "recent_nxdomain", rcode: dns.RcodeNameError, age: 5 * time.Second, keep: true},
			{name: "expired_nodata", age: time.Minute},
			{name: "expired_nxdomain", rcode: dns.RcodeNameError, age: time.Minute},
			{name: "zero_soa", zeroSOA: true},
			{name: "truncated", truncated: true},
			{name: "recent_signed", signed: true, age: 5 * time.Second, keep: true},
			{name: "expired_signed", signed: true, age: time.Minute},
			{name: "recent_ad", ad: true, age: 5 * time.Second, keep: true},
			{name: "expired_ad", ad: true, age: time.Minute},
		} {
			t.Run(storage+"/"+tc.name, func(t *testing.T) {
				args := persistenceTestArgs(t.TempDir())
				source := NewCache(args, Opts{})
				qCtx := responseTTLQuery(tc.rcode, false, true, 600, 30)
				if tc.signed {
					qCtx = signedPolicyQuery(t)
				}
				if tc.ad {
					qCtx = testQueryContextWithTTL(t, "authenticated.example.", net.IPv4(1, 2, 3, 4), 30)
					qCtx.R().AuthenticatedData = true
				}
				if tc.zeroSOA {
					qCtx.R().Ns[0].(*dns.SOA).Minttl = 0
				}
				qCtx.R().Truncated = tc.truncated
				payload, err := qCtx.R().Pack()
				if err != nil {
					t.Fatal(err)
				}
				stored := time.Now().Add(-tc.age).Truncate(time.Second)
				legacy := &item{resp: payload, storedUnixNano: stored.UnixNano(), expireUnixNano: stored.Add(300 * time.Second).UnixNano()}
				legacy.refreshTTLOffsets()
				k := key(cacheKeyForQuery(t, "negative.example."))
				source.backend.Store(k, legacy, stored.Add(12*time.Hour))
				if storage == "dump" {
					// Emit the old unnormalized entry without running a current checkpoint.
					if _, err := source.writeSnapshotFileAtomic(args.DumpFile); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := source.persistence.appendStore(walStoreRecord{key: k, cacheExp: stored.Add(12 * time.Hour), cacheItem: legacy}); err != nil {
						t.Fatal(err)
					}
					if err := source.persistence.close(); err != nil {
						t.Fatal(err)
					}
				}
				// Close only the in-memory backend: an old process could have left
				// exactly these files after an abrupt exit.
				source.closeOnce.Do(func() { close(source.closeNotify) })
				_ = source.persistence.close()
				_ = source.backend.Close()
				restored := NewCache(args, Opts{})
				defer restored.Close()
				if restored.persistence.restoreErr != nil {
					t.Fatal(restored.persistence.restoreErr)
				}
				v, expiration, exists := restored.backend.Get(k)
				if exists != tc.keep {
					t.Fatalf("recovered legacy entry = %v, want %v", exists, tc.keep)
				}
				if tc.keep {
					limit := stored.Add(30 * time.Second)
					if v.expireUnixNano > limit.UnixNano() || expiration.After(limit) {
						t.Fatal("legacy response lifetime still exceeds its original limit")
					}
					msg, _, _, _ := restored.respFromCacheItem(v, 43200, expiredMsgTtl)
					if msg == nil {
						t.Fatal("recent restored response was lost")
					}
					if tc.signed || tc.ad {
						if !v.dnssec || msg.Answer[0].Header().Ttl > 30 {
							t.Fatal("legacy authentication cache metadata or TTL was not bounded")
						}
					} else if msg.Ns[0].Header().Ttl > 30 {
						t.Fatal("legacy SOA wire TTL was not bounded")
					}
				}
			})
		}
	}
}

func TestWALRecoveryRebuildsResponseTTLIndexes(t *testing.T) {
	args := &Args{Size: 256, WALFile: filepath.Join(t.TempDir(), "cache.wal")}
	source := NewCache(args, Opts{})
	k := storePersistenceAnswer(t, source, "wal-ttl.example.", net.IPv4(1, 2, 3, 4))
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	restored := NewCache(args, Opts{})
	defer restored.Close()
	v, _, ok := restored.backend.Get(key(k))
	if !ok || len(v.ttlOffsets) == 0 {
		t.Fatal("WAL recovery did not rebuild wire TTL metadata")
	}
	var original dns.Msg
	if err := original.Unpack(v.resp); err != nil {
		t.Fatal(err)
	}
	wire := buildCachedWirePayload(v, 42, false, expiredMsgTtl, v.storedUnixNano+int64(10*time.Second), 0, 0, dns.MinMsgSize)
	var aged dns.Msg
	if err := aged.Unpack(wire); err != nil || aged.Answer[0].Header().Ttl != original.Answer[0].Header().Ttl-10 {
		t.Fatal("WAL-restored UDP response did not age its TTL")
	}
}

func TestLegacyNegativeDumpImportKeepsPositiveCache(t *testing.T) {
	source := NewCache(&Args{Size: 256}, Opts{})
	defer source.Close()
	positiveKey := storePersistenceAnswer(t, source, "positive.example.", net.IPv4(1, 2, 3, 4))
	var dump bytes.Buffer
	if _, err := source.writeDump(&dump); err != nil {
		t.Fatal(err)
	}
	args := persistenceTestArgs(filepath.Join(t.TempDir(), "import"))
	restored := NewCache(args, Opts{})
	defer restored.Close()
	if _, err := restored.readDumpWithWAL(&dump, true); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := restored.backend.Get(key(positiveKey)); !ok {
		t.Fatal("normalizing legacy responses removed a valid positive entry")
	}
}

func TestLegacyPersistenceRejectsIncompatibleRequestSemantics(t *testing.T) {
	for _, storage := range []string{"dump", "wal", "import"} {
		t.Run(storage, func(t *testing.T) {
			args := persistenceTestArgs(t.TempDir())
			source := NewCache(args, Opts{})
			t.Cleanup(func() { _ = source.Close() })
			stored := time.Now().Add(-time.Second).Truncate(time.Second)
			cacheExpiry := stored.Add(12 * time.Hour)
			keys := make(map[string]key)
			for _, kind := range []string{"ordinary", "chaos", "no-rd"} {
				name := kind + ".legacy-request.example."
				q := new(dns.Msg)
				q.SetQuestion(name, dns.TypeA)
				if kind == "chaos" {
					q.Question[0].Qclass = dns.ClassCHAOS
				} else if kind == "no-rd" {
					q.RecursionDesired = false
				}
				r := new(dns.Msg)
				r.SetReply(q)
				r.Answer = []dns.RR{&dns.A{
					Hdr: dns.RR_Header{Name: name, Rrtype: dns.TypeA, Class: q.Question[0].Qclass, Ttl: 300},
					A:   net.IPv4(192, 0, 2, 1),
				}}
				wire, err := r.Pack()
				if err != nil {
					t.Fatal(err)
				}
				legacy := &item{resp: wire, storedUnixNano: stored.UnixNano(), expireUnixNano: stored.Add(300 * time.Second).UnixNano()}
				legacy.refreshTTLOffsets()
				// Before class/RD isolation all three responses used this ordinary key.
				k := key(cacheKeyForQuery(t, name))
				keys[kind] = k
				source.backend.Store(k, legacy, cacheExpiry)
				if storage == "wal" {
					if err := source.persistence.appendStore(walStoreRecord{key: k, cacheExp: cacheExpiry, cacheItem: legacy}); err != nil {
						t.Fatal(err)
					}
				}
			}
			var imported bytes.Buffer
			switch storage {
			case "dump":
				if _, err := source.writeSnapshotFileAtomic(args.DumpFile); err != nil {
					t.Fatal(err)
				}
			case "import":
				if _, err := source.writeDump(&imported); err != nil {
					t.Fatal(err)
				}
				args = persistenceTestArgs(filepath.Join(t.TempDir(), "import"))
			}
			// Preserve the old files as an abruptly stopped process would leave them.
			source.closeOnce.Do(func() { close(source.closeNotify) })
			if err := source.persistence.close(); err != nil {
				t.Fatal(err)
			}
			if err := source.backend.Close(); err != nil {
				t.Fatal(err)
			}
			restored := NewCache(args, Opts{})
			defer restored.Close()
			if restored.persistence.restoreErr != nil {
				t.Fatal(restored.persistence.restoreErr)
			}
			if storage == "import" {
				if _, err := restored.readDumpWithWAL(&imported, true); err != nil {
					t.Fatal(err)
				}
			}
			for kind, k := range keys {
				_, _, exists := restored.backend.Get(k)
				if exists != (kind == "ordinary") {
					t.Errorf("restored %s response = %v", kind, exists)
				}
			}
		})
	}
}
