package domain_stats_pool

import (
	"database/sql/driver"
	"fmt"
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
	sqlite.MustRegisterScalarFunction("stats_pool_save_barrier", 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		if value, ok := persistenceBarriers.Load(args[0].(string)); ok {
			b := value.(*persistenceBarrier)
			b.once.Do(func() { close(b.entered); <-b.release })
		}
		return int64(0), nil
	})
}

func TestPersistenceKeepsConcurrentObservations(t *testing.T) {
	path := t.TempDir() + "/control.db"
	pool, err := newDomainStatsPoolWithDeps("top_domains", nil, path)
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
	if _, err := db.DB().Exec(fmt.Sprintf("CREATE TEMP TRIGGER pause_save BEFORE INSERT ON domain_pool_meta BEGIN SELECT stats_pool_save_barrier('%s'); END", key)); err != nil {
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
	state, ok, err := coremain.LoadDomainPoolStateFromPath(path, "top_domains")
	if err != nil || !ok || len(state.Domains) != 2 || state.Meta.TotalObservations != 2 {
		t.Fatalf("observation did not reach store %+v %v", state, err)
	}
}

func TestPersistenceFailureRemainsPending(t *testing.T) {
	path := t.TempDir() + "/control.db"
	pool, err := newDomainStatsPoolWithDeps("top_domains", nil, path)
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
	state, ok, err := coremain.LoadDomainPoolStateFromPath(path, "top_domains")
	if err != nil || !ok || len(state.Domains) != 1 {
		t.Fatalf("retry failed %+v %v", state, err)
	}
}
