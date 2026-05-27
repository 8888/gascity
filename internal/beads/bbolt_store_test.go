package beads_test

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/beads/beadstest"
)

func TestBboltStoreConformance(t *testing.T) {
	factory := func() beads.Store {
		return openTestBboltStore(t, t.TempDir(), "gc")
	}
	beadstest.RunStoreTests(t, factory)
	beadstest.RunSequentialIDTests(t, factory)
	beadstest.RunCreationOrderTests(t, factory)
	beadstest.RunDepTests(t, factory)
	beadstest.RunMetadataTests(t, factory)
}

func TestBboltStorePersistsRecordsWispsDepsAndSequence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "beads.bolt")

	store := openTestBboltStoreAt(t, path, "bb")
	main, err := store.Create(beads.Bead{
		Title:    "main",
		Labels:   []string{"focus"},
		Metadata: map[string]string{"phase": "one"},
	})
	if err != nil {
		t.Fatalf("Create main: %v", err)
	}
	wisp, err := store.Create(beads.Bead{
		Title:     "wisp",
		Type:      "message",
		Ephemeral: true,
		Metadata:  map[string]string{"phase": "one"},
	})
	if err != nil {
		t.Fatalf("Create wisp: %v", err)
	}
	if err := store.DepAdd(main.ID, wisp.ID, "tracks"); err != nil {
		t.Fatalf("DepAdd: %v", err)
	}
	if err := store.SetMetadataBatch(main.ID, map[string]string{"phase": "two", "owner": "builder"}); err != nil {
		t.Fatalf("SetMetadataBatch: %v", err)
	}
	if err := store.Close(main.ID); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := store.Shutdown(); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	reopened := openTestBboltStoreAt(t, path, "bb")
	gotMain, err := reopened.Get(main.ID)
	if err != nil {
		t.Fatalf("Get main after reopen: %v", err)
	}
	if gotMain.Status != "closed" {
		t.Fatalf("main status after reopen = %q, want closed", gotMain.Status)
	}
	if gotMain.Metadata["phase"] != "two" || gotMain.Metadata["owner"] != "builder" {
		t.Fatalf("main metadata after reopen = %#v", gotMain.Metadata)
	}
	wisps, err := reopened.ListByMetadata(map[string]string{"phase": "one"}, 0, beads.WithEphemeral)
	if err != nil {
		t.Fatalf("ListByMetadata wisps: %v", err)
	}
	if len(wisps) != 1 || wisps[0].ID != wisp.ID || !wisps[0].Ephemeral {
		t.Fatalf("wisps after reopen = %#v, want ephemeral %s", wisps, wisp.ID)
	}
	deps, err := reopened.DepList(main.ID, "down")
	if err != nil {
		t.Fatalf("DepList after reopen: %v", err)
	}
	if len(deps) != 1 || deps[0].IssueID != main.ID || deps[0].DependsOnID != wisp.ID || deps[0].Type != "tracks" {
		t.Fatalf("deps after reopen = %#v", deps)
	}
	next, err := reopened.Create(beads.Bead{Title: "next"})
	if err != nil {
		t.Fatalf("Create next: %v", err)
	}
	if next.ID != "bb-3" {
		t.Fatalf("next ID = %q, want bb-3", next.ID)
	}
}

func TestBboltStoreDeleteRemovesTouchingDependencies(t *testing.T) {
	store := openTestBboltStore(t, t.TempDir(), "bb")
	first, err := store.Create(beads.Bead{Title: "first"})
	if err != nil {
		t.Fatalf("Create first: %v", err)
	}
	second, err := store.Create(beads.Bead{Title: "second"})
	if err != nil {
		t.Fatalf("Create second: %v", err)
	}
	if err := store.DepAdd(first.ID, second.ID, "blocks"); err != nil {
		t.Fatalf("DepAdd: %v", err)
	}
	if err := store.Delete(second.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := store.Get(second.ID); !errors.Is(err, beads.ErrNotFound) {
		t.Fatalf("Get deleted error = %v, want ErrNotFound", err)
	}
	deps, err := store.DepList(first.ID, "down")
	if err != nil {
		t.Fatalf("DepList: %v", err)
	}
	if len(deps) != 0 {
		t.Fatalf("deps after deleting target = %#v, want empty", deps)
	}
}

func TestBboltStorePersistsDepTypeReplacement(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "beads.bolt")
	store := openTestBboltStoreAt(t, path, "bb")
	if err := store.DepAdd("bb-1", "bb-2", "blocks"); err != nil {
		t.Fatalf("DepAdd blocks: %v", err)
	}
	if err := store.DepAdd("bb-1", "bb-2", "tracks"); err != nil {
		t.Fatalf("DepAdd tracks: %v", err)
	}
	if err := store.Shutdown(); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	reopened := openTestBboltStoreAt(t, path, "bb")
	deps, err := reopened.DepList("bb-1", "down")
	if err != nil {
		t.Fatalf("DepList after reopen: %v", err)
	}
	if len(deps) != 1 || deps[0].Type != "tracks" {
		t.Fatalf("deps after reopen = %#v, want one tracks dependency", deps)
	}
}

func TestBboltStorePreservesParentChildWhenReplacingBlockingDep(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "beads.bolt")
	store := openTestBboltStoreAt(t, path, "bb")
	if err := store.DepAdd("bb-child", "bb-parent", "blocks"); err != nil {
		t.Fatalf("DepAdd blocks: %v", err)
	}
	if err := store.DepAdd("bb-child", "bb-parent", "parent-child"); err != nil {
		t.Fatalf("DepAdd parent-child: %v", err)
	}
	if err := store.DepAdd("bb-child", "bb-parent", "conditional-blocks"); err != nil {
		t.Fatalf("DepAdd conditional-blocks: %v", err)
	}
	if err := store.Shutdown(); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	reopened := openTestBboltStoreAt(t, path, "bb")
	deps, err := reopened.DepList("bb-child", "down")
	if err != nil {
		t.Fatalf("DepList after reopen: %v", err)
	}
	if !hasDepType(deps, "conditional-blocks") || !hasDepType(deps, "parent-child") || len(deps) != 2 {
		t.Fatalf("deps after reopen = %#v, want conditional-blocks and parent-child", deps)
	}
}

func TestBboltStoreNormalizesNeedsDepsOnCreate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "beads.bolt")
	store := openTestBboltStoreAt(t, path, "bb")
	created, err := store.Create(beads.Bead{
		Title: "needs",
		Needs: []string{"blocks:bb-target", "tracks:bb-target"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := store.Shutdown(); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	reopened := openTestBboltStoreAt(t, path, "bb")
	deps, err := reopened.DepList(created.ID, "down")
	if err != nil {
		t.Fatalf("DepList after reopen: %v", err)
	}
	if len(deps) != 1 || deps[0].DependsOnID != "bb-target" || deps[0].Type != "tracks" {
		t.Fatalf("deps after reopen = %#v, want one tracks dependency", deps)
	}
}

func openTestBboltStore(t *testing.T, dir, prefix string) *beads.BboltStore {
	t.Helper()
	return openTestBboltStoreAt(t, filepath.Join(dir, "beads.bolt"), prefix)
}

func openTestBboltStoreAt(t *testing.T, path, prefix string) *beads.BboltStore {
	t.Helper()
	store, err := beads.OpenBboltStore(path, beads.WithBboltStoreIDPrefix(prefix))
	if err != nil {
		t.Fatalf("OpenBboltStore: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Shutdown(); err != nil {
			t.Fatalf("Shutdown cleanup: %v", err)
		}
	})
	return store
}

func hasDepType(deps []beads.Dep, depType string) bool {
	for _, dep := range deps {
		if dep.Type == depType {
			return true
		}
	}
	return false
}
