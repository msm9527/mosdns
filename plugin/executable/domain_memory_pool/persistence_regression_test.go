package domain_memory_pool

import (
	"context"
	"database/sql/driver"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	runtimesqlite "github.com/IrineSistiana/mosdns/v5/internal/store/sqlite"
	sqlite "modernc.org/sqlite"
)

type persistenceBarrier struct {
	entered, release chan struct{}
	once             sync.Once
}

var persistenceBarriers sync.Map

func init() {
	sqlite.MustRegisterScalarFunction("memory_pool_save_barrier", 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		if value, ok := persistenceBarriers.Load(args[0].(string)); ok {
			b := value.(*persistenceBarrier)
			b.once.Do(func() { close(b.entered); <-b.release })
		}
		return int64(0), nil
	})
}

func TestPersistenceKeepsConcurrentObservations(t *testing.T) {
	path := t.TempDir() + "/control.db"
	pool, err := newDomainMemoryPoolWithDeps("my_realiplist", nil, nil, nil, path)
	if err != nil {
		t.Fatal(err)
	}
	pool.processRecord(&logItem{name: "before.example", source: "live", qtype: 1})
	db, err := runtimesqlite.OpenPersistent(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	key := fmt.Sprintf("%s-%d", t.Name(), time.Now().UnixNano())
	barrier := &persistenceBarrier{entered: make(chan struct{}), release: make(chan struct{})}
	persistenceBarriers.Store(key, barrier)
	defer persistenceBarriers.Delete(key)
	defer runtimesqlite.ResetPersistent(path)
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(barrier.release) }) }
	defer release()
	if _, err := db.DB().Exec(fmt.Sprintf("CREATE TEMP TRIGGER pause_save BEFORE INSERT ON domain_pool_meta BEGIN SELECT memory_pool_save_barrier('%s'); END", key)); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- pool.performWrite(WriteModeSave) }()
	select {
	case <-barrier.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("save never reached barrier")
	}
	// The SQL barrier runs after the pool snapshot has released its mutex.
	pool.processRecord(&logItem{name: "during.example", source: "live", qtype: 28})
	release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !pool.dirtyPending.Load() {
		t.Fatal("concurrent observation lost its persistence signal")
	}
	if _, err := db.DB().Exec("DROP TRIGGER pause_save"); err != nil {
		t.Fatal(err)
	}
	if err := pool.performWrite(WriteModePeriodic); err != nil {
		t.Fatal(err)
	}
	state, ok, err := coremain.LoadDomainPoolStateFromPath(path, "my_realiplist")
	if err != nil || !ok || len(state.Domains) != 2 || state.Meta.TotalObservations != 2 {
		t.Fatalf("observation did not reach store %+v %v", state, err)
	}
}

func TestPersistenceFailureRemainsPending(t *testing.T) {
	path := t.TempDir() + "/control.db"
	pool, err := newDomainMemoryPoolWithDeps("my_realiplist", nil, nil, nil, path)
	if err != nil {
		t.Fatal(err)
	}
	defer runtimesqlite.ResetPersistent(path)
	pool.processRecord(&logItem{name: "retry.example", qtype: 1})
	db, err := runtimesqlite.OpenPersistent(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB().Exec("CREATE TEMP TRIGGER fail_save BEFORE INSERT ON domain_pool_meta BEGIN SELECT RAISE(ABORT, 'injected failure'); END"); err != nil {
		t.Fatal(err)
	}
	if err := pool.performWrite(WriteModeSave); err == nil {
		t.Fatal("expected save failure")
	}
	if !pool.dirtyPending.Load() {
		t.Fatal("failed save lost pending state")
	}
	if _, err := db.DB().Exec("DROP TRIGGER fail_save"); err != nil {
		t.Fatal(err)
	}
	if err := pool.performWrite(WriteModePeriodic); err != nil {
		t.Fatal(err)
	}
	state, ok, err := coremain.LoadDomainPoolStateFromPath(path, "my_realiplist")
	if err != nil || !ok || len(state.Domains) != 1 {
		t.Fatalf("retry failed %+v %v", state, err)
	}
}

func TestBatchVerifyPersistsOnceAndKeepsMissingIndependent(t *testing.T) {
	path := t.TempDir() + "/control.db"
	pool, err := newDomainMemoryPoolWithDeps("my_realiplist", nil, nil, nil, path)
	if err != nil {
		t.Fatal(err)
	}
	defer runtimesqlite.ResetPersistent(path)
	domains := make([]string, 32)
	for i := range domains {
		domains[i] = fmt.Sprintf("batch-%d.example", i)
		pool.processRecord(&logItem{name: domains[i], source: "live", qtype: 1})
	}
	db, err := runtimesqlite.OpenPersistent(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB().Exec("CREATE TEMP TABLE saves(n INTEGER); INSERT INTO saves VALUES(0); CREATE TEMP TRIGGER count_saves BEFORE INSERT ON domain_pool_meta BEGIN UPDATE saves SET n=n+1; END"); err != nil {
		t.Fatal(err)
	}
	n, err := pool.MarkDomainsVerified(context.Background(), append(domains, "missing.example"), "2026-10-05T00:00:00Z")
	if err == nil || n != 32 {
		t.Fatalf("partial verify n=%d err=%v", n, err)
	}
	var saves int
	if err := db.DB().QueryRow("SELECT n FROM saves").Scan(&saves); err != nil {
		t.Fatal(err)
	}
	if saves != 1 {
		t.Fatalf("32 domains used %d pool saves", saves)
	}
	state, _, err := coremain.LoadDomainPoolStateFromPath(path, "my_realiplist")
	if err != nil {
		t.Fatal(err)
	}
	for _, variant := range state.Variants {
		if variant.RefreshState != "clean" || variant.LastVerifiedAtUnixMS != parseStampUnixMS("2026-10-05T00:00:00Z") || variant.CooldownUntilUnixMS != 0 {
			t.Fatalf("verify not durable %+v", variant)
		}
	}
}

func TestMemoryPoolCrashRecovery(t *testing.T) {
	if path := os.Getenv("MOSDNS_TEST_POOL_CRASH_PATH"); path != "" {
		pool, err := newDomainMemoryPoolWithDeps("my_realiplist", nil, nil, nil, path)
		if err != nil {
			t.Fatal(err)
		}
		for _, domain := range []string{"verified.example", "cooldown.example"} {
			for i := 0; i < 4; i++ {
				pool.processRecord(&logItem{name: domain, source: "live", qtype: 1})
			}
		}
		if _, err := pool.MarkDomainVerified(context.Background(), "verified.example", "2026-10-05T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
		// Exit without shutdown, closing SQLite, or checkpointing its WAL.
		os.Exit(0)
	}
	path := t.TempDir() + "/control.db"
	cmd := exec.Command(os.Args[0], "-test.run=^TestMemoryPoolCrashRecovery$")
	cmd.Env = append(os.Environ(), "MOSDNS_TEST_POOL_CRASH_PATH="+path)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("crash writer %v %s", err, output)
	}
	pool, err := newDomainMemoryPoolWithDeps("my_realiplist", nil, nil, nil, path)
	if err != nil {
		t.Fatal(err)
	}
	defer runtimesqlite.ResetPersistent(path)
	if err := pool.loadFromStore(); err != nil {
		t.Fatal(err)
	}
	verified := pool.stats[buildEntryKey("verified.example", 0)]
	dirty := pool.stats[buildEntryKey("cooldown.example", 0)]
	if verified == nil || !verified.Promoted || verified.RefreshState != "clean" || verified.CooldownUntilUnixMS != 0 || verified.LastVerifiedAtUnixMS != parseStampUnixMS("2026-10-05T00:00:00Z") {
		t.Fatalf("verified state did not survive exit %+v", verified)
	}
	if dirty == nil || !dirty.Promoted || dirty.RefreshState != "dirty" || dirty.CooldownUntilUnixMS <= dirty.LastDirtyAtUnixMS {
		t.Fatalf("cooldown/promotion did not survive exit %+v", dirty)
	}
}
