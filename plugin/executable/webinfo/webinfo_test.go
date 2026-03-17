package webinfo

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/IrineSistiana/mosdns/v5/coremain"
)

func TestWebInfoStoresOnlyInRuntimeDB(t *testing.T) {
	t.Parallel()

	filePath := filepath.Join(t.TempDir(), "webinfo", "clientname.json")
	raw, err := newWebinfo(nil, &Args{File: filePath})
	if err != nil {
		t.Fatalf("newWebinfo: %v", err)
	}
	p := raw.(*WebInfo)

	if err := p.ReplaceJSONValue(context.Background(), map[string]any{"clientA": "lab-host"}); err != nil {
		t.Fatalf("ReplaceJSONValue: %v", err)
	}
	if _, err := os.Stat(filePath); !os.IsNotExist(err) {
		t.Fatalf("expected no webinfo file write, got err=%v", err)
	}

	var stored map[string]string
	ok, err := coremain.LoadRuntimeStateJSONFromPath(
		coremain.RuntimeStateDBPathForPath(filePath),
		runtimeStateNamespaceWebinfo,
		filepath.Clean(filePath),
		&stored,
	)
	if err != nil {
		t.Fatalf("LoadRuntimeStateJSONFromPath: %v", err)
	}
	if !ok {
		t.Fatal("expected webinfo payload in runtime DB")
	}
	if stored["clientA"] != "lab-host" {
		t.Fatalf("unexpected stored payload: %+v", stored)
	}
}
