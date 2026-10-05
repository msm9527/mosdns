package cache

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/miekg/dns"
	"gopkg.in/yaml.v3"
)

func TestCacheWALSyncConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name     string
		config   string
		interval time.Duration
	}{
		{name: "omitted", interval: 60 * time.Second},
		{name: "zero", config: "wal_sync_interval: 0\n", interval: 60 * time.Second},
		{name: "explicit_one", config: "wal_sync_interval: 1\n", interval: time.Second},
		{name: "explicit_longer", config: "wal_sync_interval: 300\n", interval: 300 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := new(Args)
			if err := yaml.Unmarshal([]byte("size: 32768\n"+tc.config), args); err != nil {
				t.Fatal(err)
			}
			args.WALFile = filepath.Join(t.TempDir(), "cache.wal")
			c := NewCache(args, Opts{})
			t.Cleanup(func() { _ = c.Close() })
			if c.persistence.syncInterval != tc.interval {
				t.Fatalf("effective sync interval = %v, want %v", c.persistence.syncInterval, tc.interval)
			}

			firstKey := storePersistenceAnswer(t, c, "first.example.", net.IPv4(1, 2, 3, 4))
			// Advance the interval boundary without a sleep or a wall-clock race.
			c.persistence.mu.Lock()
			c.persistence.lastSync = time.Now().Add(-2 * time.Second)
			c.persistence.mu.Unlock()
			secondKey := storePersistenceAnswer(t, c, "second.example.", net.IPv4(4, 3, 2, 1))

			beforeClose := NewCache(args, Opts{})
			_, _, visible := beforeClose.backend.Get(key(secondKey))
			if visible != (tc.interval == time.Second) {
				t.Fatalf("record visible before close = %v for interval %v", visible, tc.interval)
			}
			if err := beforeClose.Close(); err != nil {
				t.Fatal(err)
			}
			if err := c.Close(); err != nil {
				t.Fatal(err)
			}

			replayed := NewCache(args, Opts{})
			defer replayed.Close()
			for _, expected := range []struct {
				key string
				ip  net.IP
			}{
				{key: firstKey, ip: net.IPv4(1, 2, 3, 4)},
				{key: secondKey, ip: net.IPv4(4, 3, 2, 1)},
			} {
				resp, lazy, _ := getRespFromCache(expected.key, replayed.backend, 0, expiredMsgTtl)
				if resp == nil || lazy || len(resp.Answer) != 1 {
					t.Fatal("close did not preserve a fresh answer")
				}
				answer, ok := resp.Answer[0].(*dns.A)
				if !ok || !answer.A.Equal(expected.ip) {
					t.Fatal("replayed answer differs from the stored response")
				}
			}
		})
	}
}

func TestShippedCachePolicyWALSyncInterval(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "config", "sub_config", "cache_policies.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var policy struct {
		Response map[string]*Args `yaml:"response"`
	}
	if err := yaml.Unmarshal(data, &policy); err != nil {
		t.Fatal(err)
	}
	args := policy.Response["cache_main"]
	if args == nil {
		t.Fatal("shipped policy has no main response cache")
	}
	args.DumpFile = filepath.Join(t.TempDir(), "cache.dump")
	args.WALFile = ""
	c := NewCache(args, Opts{})
	defer c.Close()
	if c.persistence.syncInterval != 60*time.Second {
		t.Fatalf("shipped policy effective sync interval = %v, want 60s", c.persistence.syncInterval)
	}
}
