package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
)

// CoordStoreBackendCheck reports the configured coordination-store backend.
type CoordStoreBackendCheck struct {
	cityPath string
	cfg      *config.City
}

// NewCoordStoreBackendCheck returns an informational backend check.
func NewCoordStoreBackendCheck(cityPath string, cfg *config.City) *CoordStoreBackendCheck {
	return &CoordStoreBackendCheck{cityPath: cityPath, cfg: cfg}
}

// Name returns the check identifier shown by gc doctor.
func (c *CoordStoreBackendCheck) Name() string { return "coord-store-backend" }

// Run reports the active coordination-store backend.
func (c *CoordStoreBackendCheck) Run(_ *CheckContext) *CheckResult {
	r := &CheckResult{Name: c.Name()}
	backend := ""
	if c.cfg != nil {
		backend = strings.ToLower(strings.TrimSpace(c.cfg.Beads.Backend))
	}
	switch backend {
	case "", "dolt":
		r.Status = StatusOK
		r.Message = "coord-store backend: dolt (default)"
	case "bbolt":
		path := beads.BboltStorePath(c.cityPath)
		r.Status = StatusOK
		if _, err := os.Stat(path); err == nil {
			r.Message = fmt.Sprintf("coord-store backend: bbolt (%s)", path)
		} else {
			r.Message = fmt.Sprintf("coord-store backend: bbolt (%s; store not yet created)", path)
		}
	default:
		r.Status = StatusWarning
		r.Message = fmt.Sprintf("coord-store backend: unknown value %q", backend)
		r.FixHint = "set [beads] backend to empty string, \"dolt\", or \"bbolt\""
	}
	if c.cityPath != "" {
		r.Details = append(r.Details, "store path: "+filepath.Clean(beads.BboltStorePath(c.cityPath)))
	}
	return r
}

// CanFix reports whether this check supports automatic remediation.
func (c *CoordStoreBackendCheck) CanFix() bool { return false }

// Fix is not supported for the informational backend check.
func (c *CoordStoreBackendCheck) Fix(_ *CheckContext) error { return nil }

// WarmupEligible reports whether the check participates in warm-up scans.
func (c *CoordStoreBackendCheck) WarmupEligible() bool { return false }
