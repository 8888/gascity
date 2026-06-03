package config

import (
	"reflect"
	"testing"
)

func TestMergePackDirsForRig(t *testing.T) {
	city := []string{"/city/core", "/city/maintenance"}
	rigDirs := map[string][]string{
		"alpha": {"/rigs/alpha/pack"},
		"beta":  {"/rigs/beta/pack", "/city/core"}, // overlap with city — must dedup
	}

	tests := []struct {
		name    string
		rigName string
		want    []string
	}{
		{
			name:    "single rig appends only that rig's dirs after city dirs",
			rigName: "alpha",
			want:    []string{"/city/core", "/city/maintenance", "/rigs/alpha/pack"},
		},
		{
			name:    "single rig dedups dirs already present at city level",
			rigName: "beta",
			want:    []string{"/city/core", "/city/maintenance", "/rigs/beta/pack"},
		},
		{
			name:    "empty rig name falls back to all rigs, sorted by rig name",
			rigName: "",
			want:    []string{"/city/core", "/city/maintenance", "/rigs/alpha/pack", "/rigs/beta/pack"},
		},
		{
			name:    "unknown rig yields city dirs only",
			rigName: "missing",
			want:    []string{"/city/core", "/city/maintenance"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := MergePackDirsForRig(city, rigDirs, tc.rigName)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("MergePackDirsForRig(%q) = %v, want %v", tc.rigName, got, tc.want)
			}
		})
	}
}

func TestCityPackDirsForRigUsesRigImports(t *testing.T) {
	c := &City{
		PackDirs:    []string{"/city/core"},
		RigPackDirs: map[string][]string{"docnow": {"/rigs/docnow/pack"}},
	}
	// A rig-scoped agent sees city dirs + its own rig's pack dirs.
	if got, want := c.PackDirsForRig("docnow"), []string{"/city/core", "/rigs/docnow/pack"}; !reflect.DeepEqual(got, want) {
		t.Errorf("PackDirsForRig(docnow) = %v, want %v", got, want)
	}
	// A city-scoped agent (no rig) still gets rig-imported dirs via AllPackDirs.
	if got, want := c.PackDirsForRig(""), []string{"/city/core", "/rigs/docnow/pack"}; !reflect.DeepEqual(got, want) {
		t.Errorf("PackDirsForRig(\"\") = %v, want %v", got, want)
	}
	if got, want := c.AllPackDirs(), []string{"/city/core", "/rigs/docnow/pack"}; !reflect.DeepEqual(got, want) {
		t.Errorf("AllPackDirs() = %v, want %v", got, want)
	}
}
