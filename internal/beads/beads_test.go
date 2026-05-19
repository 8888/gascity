package beads

import (
	"testing"
	"time"
)

var (
	_ Tx = (*BdStore)(nil)
	_ Tx = (*CachingStore)(nil)
	_ Tx = (*FileStore)(nil)
	_ Tx = (*MemStore)(nil)
)

func TestIsContainerType(t *testing.T) {
	tests := []struct {
		typ  string
		want bool
	}{
		{"convoy", true},
		{"epic", false},
		{"task", false},
		{"message", false},
		{"", false},
		{"CONVOY", false}, // case-sensitive
	}
	for _, tt := range tests {
		if got := IsContainerType(tt.typ); got != tt.want {
			t.Errorf("IsContainerType(%q) = %v, want %v", tt.typ, got, tt.want)
		}
	}
}

func TestIsMoleculeType(t *testing.T) {
	tests := []struct {
		typ  string
		want bool
	}{
		{"molecule", true},
		{"wisp", true},
		{"task", false},
		{"convoy", false},
		{"step", false},
		{"", false},
		{"MOLECULE", false}, // case-sensitive
	}
	for _, tt := range tests {
		if got := IsMoleculeType(tt.typ); got != tt.want {
			t.Errorf("IsMoleculeType(%q) = %v, want %v", tt.typ, got, tt.want)
		}
	}
}

func TestIsReadyExcludedType(t *testing.T) {
	tests := []struct {
		typ  string
		want bool
	}{
		{"merge-request", true},
		{"gate", true},
		{"molecule", true},
		{"step", true},
		{"message", true},
		{"session", true},
		{"agent", true},
		{"role", true},
		{"rig", true},
		{"task", false},
		{"convoy", false},
		{"wisp", false},
		{"", false},
		{"MOLECULE", false}, // case-sensitive
	}
	for _, tt := range tests {
		if got := IsReadyExcludedType(tt.typ); got != tt.want {
			t.Errorf("IsReadyExcludedType(%q) = %v, want %v", tt.typ, got, tt.want)
		}
	}
}

func TestIsReadyExcludedBead(t *testing.T) {
	tests := []struct {
		name string
		bead Bead
		want bool
	}{
		{
			name: "task is actionable",
			bead: Bead{Type: "task"},
		},
		{
			name: "no-history task is actionable",
			bead: Bead{Type: "task", NoHistory: true},
		},
		{
			name: "ephemeral task is actionable",
			bead: Bead{Type: "task", Ephemeral: true},
		},
		{
			name: "session type is infrastructure",
			bead: Bead{Type: "session"},
			want: true,
		},
		{
			name: "session label is infrastructure even on task type",
			bead: Bead{Type: "task", Labels: []string{"gc:session"}},
			want: true,
		},
		{
			name: "wisp label is infrastructure even on task type",
			bead: Bead{Type: "task", Labels: []string{"gc:wisp"}},
			want: true,
		},
		{
			name: "wisp metadata is infrastructure even without label",
			bead: Bead{Type: "task", Metadata: map[string]string{"gc.kind": "wisp"}},
			want: true,
		},
		{
			name: "routed wisp root is actionable",
			bead: Bead{
				Type:     "task",
				Labels:   []string{"gc:wisp"},
				Metadata: map[string]string{"gc.kind": "wisp", "gc.routed_to": "mayor"},
			},
			want: false,
		},
		{
			name: "assigned wisp root is actionable",
			bead: Bead{
				Type:     "task",
				Labels:   []string{"gc:wisp"},
				Assignee: "mayor",
				Metadata: map[string]string{"gc.kind": "wisp"},
			},
			want: false,
		},
		{
			name: "order tracking label is infrastructure",
			bead: Bead{Type: "task", Labels: []string{"gc:order-tracking"}},
			want: true,
		},
		{
			name: "legacy order tracking label is infrastructure",
			bead: Bead{Type: "task", Labels: []string{"order-tracking"}},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsReadyExcludedBead(tt.bead); got != tt.want {
				t.Fatalf("IsReadyExcludedBead(%+v) = %v, want %v", tt.bead, got, tt.want)
			}
		})
	}
}

func TestListQueryCreatedBeforeFiltersBeforeLimit(t *testing.T) {
	base := time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC)
	items := []Bead{
		{ID: "newer-2", Title: "newer 2", Status: "closed", CreatedAt: base.Add(2 * time.Minute), Labels: []string{"order-run:digest"}},
		{ID: "newer-1", Title: "newer 1", Status: "closed", CreatedAt: base.Add(time.Minute), Labels: []string{"order-run:digest"}},
		{ID: "older-2", Title: "older 2", Status: "closed", CreatedAt: base.Add(-2 * time.Minute), Labels: []string{"order-run:digest"}},
		{ID: "older-1", Title: "older 1", Status: "closed", CreatedAt: base.Add(-time.Minute), Labels: []string{"order-run:digest"}},
	}

	got := ApplyListQuery(items, ListQuery{
		Label:         "order-run:digest",
		CreatedBefore: base,
		Limit:         1,
		IncludeClosed: true,
		Sort:          SortCreatedDesc,
	})

	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1: %+v", len(got), got)
	}
	if got[0].ID != "older-1" {
		t.Fatalf("got[0].ID = %q, want older-1", got[0].ID)
	}
}
