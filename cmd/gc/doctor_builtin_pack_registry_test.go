package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/gastownhall/gascity/internal/builtinpacks"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/doctor"
	"github.com/gastownhall/gascity/internal/fsys"
	"github.com/gastownhall/gascity/internal/packman"
)

func TestBuiltinPackRegistryMigrationCheckReportsLegacyAndMissingDefaults(t *testing.T) {
	cityDir := t.TempDir()
	writeCityToml(t, cityDir, `[workspace]
name = "demo"

[beads]
provider = "file"
`)
	writePackToml(t, cityDir, `[pack]
name = "demo"
schema = 2

[imports.gastown]
source = ".gc/system/packs/gastown"
`)

	result := newBuiltinPackRegistryMigrationCheck(cityDir).Run(&doctor.CheckContext{CityPath: cityDir})
	if result.Status != doctor.StatusError {
		t.Fatalf("status = %v, want error; result=%#v", result.Status, result)
	}
	joined := strings.Join(append([]string{result.Message}, result.Details...), "\n")
	if !strings.Contains(joined, ".gc/system/packs/gastown") {
		t.Fatalf("result missing legacy source detail: %#v", result)
	}
	if !strings.Contains(joined, "core") {
		t.Fatalf("result missing core default detail: %#v", result)
	}
	if result.FixHint == "" || !newBuiltinPackRegistryMigrationCheck(cityDir).CanFix() {
		t.Fatalf("result = %#v, want fix hint and fixable check", result)
	}
}

func TestBuiltinPackRegistryMigrationCheckFixRewritesAndInstallsBundledImports(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cityDir := t.TempDir()
	writeCityToml(t, cityDir, `[workspace]
name = "demo"

[beads]
provider = "file"

[imports.core]
source = ".gc/system/packs/core"

[[rigs]]
name = "frontend"
path = "../frontend"

[rigs.imports.gastown]
source = ".gc/system/packs/gastown"
`)
	writePackToml(t, cityDir, `[pack]
name = "demo"
schema = 2

[imports.gastown]
source = ".gc/system/packs/gastown"

[defaults.rig.imports.gastown]
source = ".gc/system/packs/gastown"
`)

	check := newBuiltinPackRegistryMigrationCheck(cityDir)
	if err := check.Fix(&doctor.CheckContext{CityPath: cityDir}); err != nil {
		t.Fatalf("Fix: %v", err)
	}
	result := check.Run(&doctor.CheckContext{CityPath: cityDir})
	if result.Status != doctor.StatusOK {
		t.Fatalf("status after fix = %v, want OK; result=%#v", result.Status, result)
	}

	var packCfg initPackConfig
	packData, err := os.ReadFile(filepath.Join(cityDir, "pack.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := toml.Decode(string(packData), &packCfg); err != nil {
		t.Fatalf("decode pack.toml: %v", err)
	}
	if got := packCfg.Imports["gastown"].Source; got != builtinpacks.MustSource("gastown") {
		t.Fatalf("pack imports.gastown.source = %q", got)
	}
	if got := packCfg.Defaults.Rig.Imports["gastown"].Source; got != builtinpacks.MustSource("gastown") {
		t.Fatalf("defaults.rig.imports.gastown.source = %q", got)
	}

	cityCfg, err := config.Load(fsys.OSFS{}, filepath.Join(cityDir, "city.toml"))
	if err != nil {
		t.Fatalf("load city.toml: %v", err)
	}
	if got := cityCfg.Imports["core"].Source; got != builtinpacks.MustSource("core") {
		t.Fatalf("city imports.core.source = %q", got)
	}
	if got := cityCfg.Rigs[0].Imports["gastown"].Source; got != builtinpacks.MustSource("gastown") {
		t.Fatalf("rig imports.gastown.source = %q", got)
	}

	lock, err := packman.ReadLockfile(fsys.OSFS{}, cityDir)
	if err != nil {
		t.Fatalf("ReadLockfile: %v", err)
	}
	for _, source := range []string{builtinpacks.MustSource("core"), builtinpacks.MustSource("gastown")} {
		entry, ok := lock.Packs[source]
		if !ok || entry.Commit == "" {
			t.Fatalf("lock missing %s: %#v", source, lock.Packs)
		}
		cacheDir, err := packman.RepoCachePath(source, entry.Commit)
		if err != nil {
			t.Fatalf("RepoCachePath: %v", err)
		}
		if err := builtinpacks.ValidateSyntheticRepo(cacheDir, entry.Commit); err != nil {
			t.Fatalf("synthetic cache for %s: %v", source, err)
		}
	}
}
