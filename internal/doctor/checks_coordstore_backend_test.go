package doctor

import (
	"os"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
)

func TestCoordStoreBackendCheckReportsBboltPath(t *testing.T) {
	cityDir := t.TempDir()
	store, err := beads.OpenBboltStore(beads.BboltStorePath(cityDir))
	if err != nil {
		t.Fatalf("OpenBboltStore: %v", err)
	}
	if err := store.Shutdown(); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	check := NewCoordStoreBackendCheck(cityDir, &config.City{
		Beads: config.BeadsConfig{Backend: "bbolt"},
	})
	got := check.Run(&CheckContext{CityPath: cityDir})
	if got.Status != StatusOK {
		t.Fatalf("status = %v, want OK", got.Status)
	}
	if !strings.Contains(got.Message, "coord-store backend: bbolt") || !strings.Contains(got.Message, beads.BboltStorePath(cityDir)) {
		t.Fatalf("message = %q, want bbolt path", got.Message)
	}
}

func TestCoordStoreBackendCheckWarnsOnUnknownBackend(t *testing.T) {
	check := NewCoordStoreBackendCheck(t.TempDir(), &config.City{
		Beads: config.BeadsConfig{Backend: "sqlite"},
	})
	got := check.Run(&CheckContext{})
	if got.Status != StatusWarning {
		t.Fatalf("status = %v, want warning", got.Status)
	}
	if !strings.Contains(got.FixHint, "bbolt") {
		t.Fatalf("FixHint = %q, want bbolt guidance", got.FixHint)
	}
}

func TestCoordStoreBackendCheckReportsDoltDefault(t *testing.T) {
	cityDir := t.TempDir()
	if err := os.MkdirAll(cityDir, 0o755); err != nil {
		t.Fatal(err)
	}
	got := NewCoordStoreBackendCheck(cityDir, &config.City{}).Run(&CheckContext{})
	if got.Status != StatusOK {
		t.Fatalf("status = %v, want OK", got.Status)
	}
	if got.Message != "coord-store backend: dolt (default)" {
		t.Fatalf("message = %q, want dolt default", got.Message)
	}
}
