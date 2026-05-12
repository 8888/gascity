package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/gastownhall/gascity/internal/builtinpacks"
	"github.com/gastownhall/gascity/internal/fsys"
	"github.com/gastownhall/gascity/internal/packman"
)

func TestLoadCityConfigWithBuiltinPacksRepairsLockedBundledCache(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cityDir := t.TempDir()
	source := builtinpacks.MustSource("bd")
	commit := "synthetic-stale"

	writeCityToml(t, cityDir, `[workspace]
name = "demo"
`)
	writePackToml(t, cityDir, fmt.Sprintf(`[pack]
name = "demo"
schema = 1

[imports.bd]
source = %q
version = "sha:%s"
`, source, commit))
	if err := packman.WriteLockfile(fsys.OSFS{}, cityDir, &packman.Lockfile{
		Schema: packman.LockfileSchema,
		Packs: map[string]packman.LockedPack{
			source: {Version: "sha:" + commit, Commit: commit},
		},
	}); err != nil {
		t.Fatalf("WriteLockfile: %v", err)
	}

	cacheDir, err := packman.RepoCachePath(source, commit)
	if err != nil {
		t.Fatalf("RepoCachePath: %v", err)
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cacheDir, ".gc-bundled-pack-cache.toml"), []byte(fmt.Sprintf(`
schema = 1
repository = %q
commit = %q
content_hash = "sha256:stale"
`, builtinpacks.Repository, commit)), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if _, _, err := loadCityConfigWithBuiltinPacks(cityDir); err != nil {
		t.Fatalf("loadCityConfigWithBuiltinPacks: %v", err)
	}
	if err := builtinpacks.ValidateSyntheticRepo(cacheDir, commit); err != nil {
		t.Fatalf("ValidateSyntheticRepo after load: %v", err)
	}
}
