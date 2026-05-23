package doctor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/config"
)

func writeRigBeadsConfig(t *testing.T, rigPath, body string) {
	t.Helper()
	beadsDir := filepath.Join(rigPath, ".beads")
	if err := os.MkdirAll(beadsDir, 0o700); err != nil {
		t.Fatalf("create .beads dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(body), 0o600); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}
}

func writeRepoStateWithRemotes(t *testing.T, doltDataDir string, remotes map[string]string) {
	t.Helper()
	writeRepoStateWithRemotesAt(t, filepath.Join(doltDataDir, "testdb"), remotes)
}

func writeRepoStateWithRemotesAt(t *testing.T, repoDir string, remotes map[string]string) {
	t.Helper()
	doltDir := filepath.Join(repoDir, ".dolt")
	if err := os.MkdirAll(doltDir, 0o700); err != nil {
		t.Fatalf("create .dolt dir: %v", err)
	}
	stateRemotes := make(map[string]any, len(remotes))
	for name, url := range remotes {
		stateRemotes[name] = map[string]string{
			"name": name,
			"url":  url,
		}
	}
	state := map[string]any{
		"head":     "refs/heads/main",
		"remotes":  stateRemotes,
		"backups":  map[string]any{},
		"branches": map[string]any{},
	}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal repo_state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(doltDir, "repo_state.json"), data, 0o600); err != nil {
		t.Fatalf("write repo_state: %v", err)
	}
}

func TestDoltLocalOnlyRemoteCheck_LocalOnlyOriginRemoteWarns(t *testing.T) {
	cityPath := t.TempDir()
	doltDataDir := filepath.Join(cityPath, ".beads", "dolt")
	rigPath := filepath.Join(cityPath, "rig")
	if err := os.MkdirAll(rigPath, 0o700); err != nil {
		t.Fatal(err)
	}
	writeRigMetadata(t, rigPath, "testdb")
	writeRigBeadsConfig(t, rigPath, "dolt.local-only: true\n")
	writeRepoStateWithRemotes(t, doltDataDir, map[string]string{
		"origin": "git+https://github.com/gastownhall/gascity.git",
	})

	c := NewDoltLocalOnlyRemoteCheck(cityPath, config.Rig{Name: "testrig", Path: rigPath}, doltDataDir)
	r := c.Run(&CheckContext{CityPath: cityPath})

	if r.Status != StatusWarning {
		t.Fatalf("status = %d, want StatusWarning; message=%s", r.Status, r.Message)
	}
	if !strings.Contains(r.Message, "origin") {
		t.Fatalf("Message should name offending remote: %s", r.Message)
	}
	if !strings.Contains(r.FixHint, "bd dolt remote remove origin") {
		t.Fatalf("FixHint should tell operator how to remove origin: %s", r.FixHint)
	}
	if !c.CanFix() {
		t.Fatal("CanFix should be true when dolt.local-only scopes the remote removal")
	}

	var removedName, removedPath string
	c.removeRemote = func(gotRigPath, gotName string) error {
		removedPath = gotRigPath
		removedName = gotName
		return nil
	}
	if err := c.Fix(&CheckContext{CityPath: cityPath}); err != nil {
		t.Fatalf("Fix() error = %v", err)
	}
	if removedName != "origin" {
		t.Fatalf("Fix removed remote %q, want origin", removedName)
	}
	if removedPath != rigPath {
		t.Fatalf("Fix used rig path %q, want %q", removedPath, rigPath)
	}
}

func TestDoltLocalOnlyRemoteCheck_LocalOnlyDirectDataDirOriginRemoteWarns(t *testing.T) {
	cityPath := t.TempDir()
	doltDataDir := filepath.Join(cityPath, ".beads", "dolt")
	rigPath := filepath.Join(cityPath, "rig")
	if err := os.MkdirAll(rigPath, 0o700); err != nil {
		t.Fatal(err)
	}
	writeRigMetadata(t, rigPath, "testdb")
	writeRigBeadsConfig(t, rigPath, "dolt.local-only: true\n")
	writeRepoStateWithRemotesAt(t, doltDataDir, map[string]string{
		"origin": "git+https://github.com/gastownhall/gascity.git",
	})

	c := NewDoltLocalOnlyRemoteCheck(cityPath, config.Rig{Name: "testrig", Path: rigPath}, doltDataDir)
	r := c.Run(&CheckContext{CityPath: cityPath})

	if r.Status != StatusWarning {
		t.Fatalf("status = %d, want StatusWarning; message=%s", r.Status, r.Message)
	}
	if !strings.Contains(r.Message, "origin") {
		t.Fatalf("Message should name offending remote in direct data dir layout: %s", r.Message)
	}
}

func TestDoltLocalOnlyRemoteCheck_LocalOnlyWithoutOffBoxRemoteOK(t *testing.T) {
	cityPath := t.TempDir()
	doltDataDir := filepath.Join(cityPath, ".beads", "dolt")
	rigPath := filepath.Join(cityPath, "rig")
	if err := os.MkdirAll(rigPath, 0o700); err != nil {
		t.Fatal(err)
	}
	writeRigMetadata(t, rigPath, "testdb")
	writeRigBeadsConfig(t, rigPath, "dolt.local-only: true\n")

	c := NewDoltLocalOnlyRemoteCheck(cityPath, config.Rig{Name: "testrig", Path: rigPath}, doltDataDir)
	r := c.Run(&CheckContext{CityPath: cityPath})

	if r.Status != StatusOK {
		t.Fatalf("status = %d, want StatusOK without repo_state remotes; message=%s", r.Status, r.Message)
	}
}

func TestDoltLocalOnlyRemoteCheck_LocalBackupRemoteOK(t *testing.T) {
	cityPath := t.TempDir()
	doltDataDir := filepath.Join(cityPath, ".beads", "dolt")
	rigPath := filepath.Join(cityPath, "rig")
	if err := os.MkdirAll(rigPath, 0o700); err != nil {
		t.Fatal(err)
	}
	writeRigMetadata(t, rigPath, "testdb")
	writeRigBeadsConfig(t, rigPath, "dolt.local-only: true\n")
	writeRepoStateWithRemotes(t, doltDataDir, map[string]string{
		"testdb-backup": "file://" + filepath.Join(cityPath, ".dolt-backup", "testdb"),
	})

	c := NewDoltLocalOnlyRemoteCheck(cityPath, config.Rig{Name: "testrig", Path: rigPath}, doltDataDir)
	r := c.Run(&CheckContext{CityPath: cityPath})

	if r.Status != StatusOK {
		t.Fatalf("status = %d, want StatusOK for local backup remote; message=%s", r.Status, r.Message)
	}
}

func TestDoltLocalOnlyRemoteCheck_FlagAbsentOrFalseAllowsRemote(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config string
	}{
		{name: "absent", config: "dolt.auto-push: false\n"},
		{name: "false", config: "dolt.local-only: false\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cityPath := t.TempDir()
			doltDataDir := filepath.Join(cityPath, ".beads", "dolt")
			rigPath := filepath.Join(cityPath, "rig")
			if err := os.MkdirAll(rigPath, 0o700); err != nil {
				t.Fatal(err)
			}
			writeRigMetadata(t, rigPath, "testdb")
			writeRigBeadsConfig(t, rigPath, tc.config)
			writeRepoStateWithRemotes(t, doltDataDir, map[string]string{
				"origin": "https://github.com/gastownhall/gascity.git",
			})

			c := NewDoltLocalOnlyRemoteCheck(cityPath, config.Rig{Name: "testrig", Path: rigPath}, doltDataDir)
			r := c.Run(&CheckContext{CityPath: cityPath})
			if r.Status != StatusOK {
				t.Fatalf("status = %d, want StatusOK without dolt.local-only:true; message=%s", r.Status, r.Message)
			}

			called := false
			c.removeRemote = func(_ string, _ string) error {
				called = true
				return nil
			}
			if err := c.Fix(&CheckContext{CityPath: cityPath}); err != nil {
				t.Fatalf("Fix() error = %v", err)
			}
			if called {
				t.Fatal("Fix removed a remote without dolt.local-only:true")
			}
		})
	}
}

func TestDoltLocalOnlyRemoteCheck_OffBoxRemoteNameWarns(t *testing.T) {
	cityPath := t.TempDir()
	doltDataDir := filepath.Join(cityPath, ".beads", "dolt")
	rigPath := filepath.Join(cityPath, "rig")
	if err := os.MkdirAll(rigPath, 0o700); err != nil {
		t.Fatal(err)
	}
	writeRigMetadata(t, rigPath, "testdb")
	writeRigBeadsConfig(t, rigPath, "dolt.local-only: true\n")
	writeRepoStateWithRemotes(t, doltDataDir, map[string]string{
		"mirror": "ssh://git@github.com/gastownhall/gascity.git",
	})

	c := NewDoltLocalOnlyRemoteCheck(cityPath, config.Rig{Name: "testrig", Path: rigPath}, doltDataDir)
	r := c.Run(&CheckContext{CityPath: cityPath})

	if r.Status != StatusWarning {
		t.Fatalf("status = %d, want StatusWarning; message=%s", r.Status, r.Message)
	}
	if !strings.Contains(r.Message, "mirror") || !strings.Contains(r.FixHint, "bd dolt remote remove mirror") {
		t.Fatalf("expected warning and fix hint to name mirror; message=%s hint=%s", r.Message, r.FixHint)
	}
}

func TestDoltLocalOnlyRemoteCheck_Name(t *testing.T) {
	c := NewDoltLocalOnlyRemoteCheck(t.TempDir(), config.Rig{Name: "myrig", Path: t.TempDir()}, "")
	want := "rig:myrig:dolt-local-only-remote"
	if got := c.Name(); got != want {
		t.Fatalf("Name() = %q, want %q", got, want)
	}
}
