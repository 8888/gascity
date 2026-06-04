package beads

import (
	"context"
	"testing"
)

func readyContainsID(ready []Bead, id string) bool {
	for _, b := range ready {
		if b.ID == id {
			return true
		}
	}
	return false
}

// TestCachedReadyStaleServesWhileDegradedAndDirty is the regression test for the
// runtime-degradation pour freeze. When the backing managed-dolt ages and its
// full-scan reconcile times out, the cache flips to cacheDegraded; pending local
// writes also leave entries in c.dirty. In that state CachedReady declines to
// answer, so the controller-demand probe would fall through to a live dolt query
// that ALSO times out — going blind and freezing all pours until the dolt is
// reaped. CachedReadyStale must instead serve the last-known-good write-through
// model so the controller can keep pouring.
func TestCachedReadyStaleServesWhileDegradedAndDirty(t *testing.T) {
	t.Parallel()

	backing := NewMemStore()
	leaf, err := backing.Create(Bead{Title: "next leaf", Assignee: "planner", Type: "task"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	cache := NewCachingStoreForTest(backing, nil)
	if err := cache.Prime(context.Background()); err != nil {
		t.Fatalf("Prime: %v", err)
	}

	// Precondition: a healthy/live cache serves the open planner leaf normally.
	if ready, ok := cache.CachedReady(); !ok || !readyContainsID(ready, leaf.ID) {
		t.Fatalf("precondition: CachedReady should serve the open planner leaf (ok=%v, ready=%+v)", ok, ready)
	}

	// Enter the degradation state: the periodic full-scan reconcile has been
	// failing (cacheDegraded) AND a local write is awaiting confirmation (dirty).
	cache.mu.Lock()
	cache.state = cacheDegraded
	cache.dirty[leaf.ID] = struct{}{}
	cache.mu.Unlock()

	// CachedReady refuses to answer here — this is exactly the condition that
	// pushes the controller-demand probe onto the slow live query and freezes.
	if _, ok := cache.CachedReady(); ok {
		t.Fatal("CachedReady should decline while cacheDegraded+dirty (the freeze trigger)")
	}

	// The fix: CachedReadyStale serves the last-known-good model so the pour
	// decision still sees the ready leaf instead of going blind.
	stale, ok := cache.CachedReadyStale()
	if !ok {
		t.Fatal("CachedReadyStale should serve the populated model while degraded")
	}
	if !readyContainsID(stale, leaf.ID) {
		t.Fatalf("CachedReadyStale missing the ready planner leaf: %+v", stale)
	}
}

// TestCachedReadyStaleDeclinesWhenModelEmpty confirms the fallback reports it
// cannot answer when no prior successful load has populated the model — callers
// must not mistake an empty model for "no work ready".
func TestCachedReadyStaleDeclinesWhenModelEmpty(t *testing.T) {
	t.Parallel()

	cache := NewCachingStoreForTest(NewMemStore(), nil)
	// No Prime → lastFreshAt zero, no beads.
	if _, ok := cache.CachedReadyStale(); ok {
		t.Fatal("CachedReadyStale should decline when the model was never loaded")
	}
}

// TestCachedReadyStaleExcludesClosedAndBlocked confirms the stale view still
// applies open/ready semantics: a closed bead and a bead with an open blocker
// are not returned.
func TestCachedReadyStaleExcludesClosedAndBlocked(t *testing.T) {
	t.Parallel()

	backing := NewMemStore()
	open, err := backing.Create(Bead{Title: "ready", Assignee: "planner", Type: "task"})
	if err != nil {
		t.Fatalf("Create ready: %v", err)
	}
	blocker, err := backing.Create(Bead{Title: "blocker", Type: "task"})
	if err != nil {
		t.Fatalf("Create blocker: %v", err)
	}
	blocked, err := backing.Create(Bead{Title: "blocked", Assignee: "planner", Type: "task"})
	if err != nil {
		t.Fatalf("Create blocked: %v", err)
	}
	if err := backing.DepAdd(blocked.ID, blocker.ID, "blocks"); err != nil {
		t.Fatalf("DepAdd: %v", err)
	}

	cache := NewCachingStoreForTest(backing, nil)
	if err := cache.Prime(context.Background()); err != nil {
		t.Fatalf("Prime: %v", err)
	}
	cache.mu.Lock()
	cache.state = cacheDegraded
	cache.mu.Unlock()

	stale, ok := cache.CachedReadyStale()
	if !ok {
		t.Fatal("CachedReadyStale should serve the populated model")
	}
	if !readyContainsID(stale, open.ID) {
		t.Fatalf("expected the unblocked open leaf to be ready: %+v", stale)
	}
	if readyContainsID(stale, blocked.ID) {
		t.Fatal("a leaf with an open blocker must not be reported ready")
	}
}
