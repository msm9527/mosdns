package coremain

import (
	"path/filepath"
	"testing"
)

func TestUpstreamRuntimeStatsStoreSaveLoadAndReset(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "control.db")

	initial := []UpstreamRuntimeStats{
		{PluginTag: "domestic", UpstreamTag: "u1", QueryTotal: 10, ErrorTotal: 1, WinnerTotal: 7, LatencyTotalUs: 50000, LatencyCount: 10},
		{PluginTag: "domestic", UpstreamTag: "u2", QueryTotal: 20, ErrorTotal: 2, WinnerTotal: 11, LatencyTotalUs: 120000, LatencyCount: 20},
		{PluginTag: "foreign", UpstreamTag: "u3", QueryTotal: 30, ErrorTotal: 3, WinnerTotal: 13, LatencyTotalUs: 210000, LatencyCount: 30},
	}
	if err := SaveUpstreamRuntimeStats(dbPath, initial); err != nil {
		t.Fatalf("SaveUpstreamRuntimeStats initial: %v", err)
	}

	values, err := LoadUpstreamRuntimeStatsByPlugin(dbPath, "domestic")
	if err != nil {
		t.Fatalf("LoadUpstreamRuntimeStatsByPlugin domestic: %v", err)
	}
	if len(values) != 2 || values["u1"].QueryTotal != 10 || values["u2"].WinnerTotal != 11 {
		t.Fatalf("unexpected domestic stats: %+v", values)
	}

	if err := SaveUpstreamRuntimeStats(dbPath, []UpstreamRuntimeStats{
		{PluginTag: "domestic", UpstreamTag: "u1", QueryTotal: 99, ErrorTotal: 4, WinnerTotal: 88, LatencyTotalUs: 990000, LatencyCount: 99},
	}); err != nil {
		t.Fatalf("SaveUpstreamRuntimeStats update: %v", err)
	}

	values, err = LoadUpstreamRuntimeStatsByPlugin(dbPath, "domestic")
	if err != nil {
		t.Fatalf("LoadUpstreamRuntimeStatsByPlugin domestic updated: %v", err)
	}
	if len(values) != 2 {
		t.Fatalf("expected save to keep unrelated rows, got %+v", values)
	}
	if values["u1"].QueryTotal != 99 || values["u2"].QueryTotal != 20 {
		t.Fatalf("unexpected upsert result: %+v", values)
	}

	deleted, err := ResetUpstreamRuntimeStats(dbPath, "domestic", "u1")
	if err != nil {
		t.Fatalf("ResetUpstreamRuntimeStats single: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("expected one deleted row, got %d", deleted)
	}

	values, err = LoadUpstreamRuntimeStatsByPlugin(dbPath, "domestic")
	if err != nil {
		t.Fatalf("LoadUpstreamRuntimeStatsByPlugin domestic after reset: %v", err)
	}
	if len(values) != 1 || values["u2"].QueryTotal != 20 {
		t.Fatalf("unexpected domestic stats after single reset: %+v", values)
	}

	deleted, err = ResetUpstreamRuntimeStats(dbPath, "", "")
	if err != nil {
		t.Fatalf("ResetUpstreamRuntimeStats all: %v", err)
	}
	if deleted != 2 {
		t.Fatalf("expected two deleted rows after global reset, got %d", deleted)
	}

	values, err = LoadUpstreamRuntimeStatsByPlugin(dbPath, "domestic")
	if err != nil {
		t.Fatalf("LoadUpstreamRuntimeStatsByPlugin domestic final: %v", err)
	}
	if len(values) != 0 {
		t.Fatalf("expected domestic stats to be empty, got %+v", values)
	}
}
