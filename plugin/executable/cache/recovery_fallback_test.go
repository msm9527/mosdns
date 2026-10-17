package cache

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/IrineSistiana/mosdns/v5/pkg/query_context"
	"github.com/IrineSistiana/mosdns/v5/plugin/executable/sequence"
	"github.com/miekg/dns"
)

func TestRecoveryFailureKeepsFreshMemoryCacheAvailable(t *testing.T) {
	for _, fault := range []string{"corrupt_snapshot", "legacy_wal_migration"} {
		for _, wire := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/wire=%v", fault, wire), func(t *testing.T) {
				args := recoveryFailureArgs(t, fault)
				originals := map[string][]byte{}
				for _, path := range []string{args.DumpFile, args.WALFile} {
					originals[path], _ = os.ReadFile(path)
				}
				c := NewCache(args, Opts{})
				defer c.Close()
				recoveryErr := c.persistence.restoreErr
				if recoveryErr == nil || c.backend.Len() != 0 {
					t.Fatal("fixture did not isolate the failed recovery")
				}
				calls, unavailable := 0, false
				raw := sequence.ExecutableFunc(func(_ context.Context, qCtx *query_context.Context) error {
					calls++
					r := new(dns.Msg)
					r.SetReply(qCtx.Q())
					if unavailable {
						r.Rcode = dns.RcodeServerFailure
					} else {
						// A validating upstream's ordinary answer cannot use UDP fast cache.
						r.AuthenticatedData = true
						r.Answer = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: qCtx.Q().Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 600}, A: net.IPv4(192, 0, 2, 1)}}
					}
					qCtx.SetResponse(r)
					return nil
				})
				m := coremain.NewTestMosdnsWithPlugins(map[string]any{"cache": c, "raw": raw})
				s, err := sequence.NewSequence(sequence.NewBQFromBP(coremain.NewBP("recovery-fallback", m)), []sequence.RuleArgs{{Exec: "$cache"}, {Exec: "$raw"}})
				if err != nil {
					t.Fatal(err)
				}
				defer s.Close()
				const name = "recovery-fallback.example."
				query := func() *dns.Msg {
					qCtx := queryThroughSequenceType(t, s, name, dns.TypeA, wire)
					if payload := qCtx.ResponsePayload(); payload != nil {
						var r dns.Msg
						if err := r.Unpack(payload.Wire); err != nil {
							t.Fatal(err)
						}
						return &r
					}
					return qCtx.R()
				}
				for i := 0; i < 3; i++ {
					if r := query(); !responseHasA(r, net.IPv4(192, 0, 2, 1)) || !r.AuthenticatedData {
						t.Fatalf("fresh response lost after recovery failure: %v", r)
					}
					unavailable = true
				}
				if calls != 1 || c.backend.Len() != 1 || c.hitCount.Load() != 2 {
					t.Fatalf("fresh memory cache unavailable: upstream=%d size=%d hits=%d", calls, c.backend.Len(), c.hitCount.Load())
				}
				if purged, err := c.PurgeDomainRuntime(context.Background(), name, dns.TypeA); err != nil || purged != 1 {
					t.Fatalf("memory purge failed in degraded persistence: count=%d err=%v", purged, err)
				}
				unavailable = false
				query()
				if calls != 2 || c.backend.Len() != 1 {
					t.Fatal("purged response did not refill from the upstream")
				}
				// A flush still clears RAM, but reports that the durable flush is blocked.
				if err := c.FlushRuntimeCache(context.Background()); !errors.Is(err, recoveryErr) || c.backend.Len() != 0 {
					t.Fatalf("flush did not clear RAM and report the persistence failure: %v", err)
				}
				query()
				unavailable = true
				if r := query(); calls != 3 || !responseHasA(r, net.IPv4(192, 0, 2, 1)) {
					t.Fatal("flush retained an L1 entry or stopped subsequent caching")
				}
				if c.snapshotStats().LastReplay.Status != "error" || c.persistence.restoreErr != recoveryErr {
					t.Fatal("memory fallback erased the recovery diagnostic")
				}
				if err := c.SaveToDisk(context.Background()); !errors.Is(err, recoveryErr) {
					t.Fatalf("explicit save did not preserve the failed recovery boundary: %v", err)
				}
				var dump bytes.Buffer
				if _, err := c.writeDump(&dump); err != nil {
					t.Fatal(err)
				}
				if _, err := c.readDumpWithWAL(&dump, true); !errors.Is(err, recoveryErr) {
					t.Fatalf("import bypassed the failed recovery boundary: %v", err)
				}
				if got := counterValue(t, c.walAppendErrorCounter); got != 0 {
					t.Fatalf("degraded memory work repeatedly attempted WAL writes: %v", got)
				}
				if err := c.Close(); err != nil {
					t.Fatal(err)
				}
				for path, before := range originals {
					after, err := os.ReadFile(path)
					if before == nil && !os.IsNotExist(err) || before != nil && (err != nil || !bytes.Equal(before, after)) {
						t.Fatalf("failed recovery changed source file %s: %v", path, err)
					}
				}
			})
		}
	}
}

func recoveryFailureArgs(t *testing.T, fault string) *Args {
	t.Helper()
	dir := t.TempDir()
	args := &Args{Size: 32768, DumpFile: filepath.Join(dir, "cache.dump"), WALFile: filepath.Join(dir, "cache.wal")}
	if fault == "corrupt_snapshot" {
		if err := os.WriteFile(args.DumpFile, []byte("invalid gzip snapshot"), 0o600); err != nil {
			t.Fatal(err)
		}
		return args
	}
	if runtime.GOOS != "linux" {
		t.Skip("migration fixture needs Linux proc file descriptors")
	}
	f, err := os.CreateTemp(dir, "legacy-wal")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	if _, err := f.WriteString(walMagic); err != nil {
		t.Fatal(err)
	}
	// The valid v1 WAL can be opened and read, but its containing proc directory
	// cannot create the replacement file, even when tests run as root.
	args.WALFile = fmt.Sprintf("/proc/self/fd/%d", f.Fd())
	return args
}
