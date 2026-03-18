package cache

import (
	"path/filepath"
	"testing"
)

func TestSnapshotConfigReflectsPersistenceState(t *testing.T) {
	persistent := NewCache(&Args{
		Size:            1024,
		DumpFile:        filepath.Join(t.TempDir(), "cache.dump"),
		DumpInterval:    30,
		WALSyncInterval: 2,
	}, Opts{})
	t.Cleanup(func() {
		_ = persistent.Close()
	})

	persistentConfig := persistent.snapshotStats().Config
	if persistentConfig["persist"] != true {
		t.Fatalf("expected persistent cache to report persist=true, got %+v", persistentConfig)
	}
	if persistentConfig["dump_interval"] != 30 || persistentConfig["wal_sync_interval"] != 2 {
		t.Fatalf("expected persistent cache to expose persistence intervals, got %+v", persistentConfig)
	}

	ephemeral := NewCache(&Args{Size: 1024}, Opts{})
	t.Cleanup(func() {
		_ = ephemeral.Close()
	})

	ephemeralConfig := ephemeral.snapshotStats().Config
	if ephemeralConfig["persist"] != false {
		t.Fatalf("expected non-persistent cache to report persist=false, got %+v", ephemeralConfig)
	}
	if _, ok := ephemeralConfig["dump_interval"]; ok {
		t.Fatalf("did not expect non-persistent cache to expose dump_interval: %+v", ephemeralConfig)
	}
	if _, ok := ephemeralConfig["wal_sync_interval"]; ok {
		t.Fatalf("did not expect non-persistent cache to expose wal_sync_interval: %+v", ephemeralConfig)
	}
}
