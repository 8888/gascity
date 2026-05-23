package doctor

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gastownhall/gascity/internal/beads/contract"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/fsys"
)

// DoltLocalOnlyRemoteCheck detects off-box Dolt remotes in rigs that
// explicitly declare dolt.local-only:true.
type DoltLocalOnlyRemoteCheck struct {
	cityPath     string
	rig          config.Rig
	doltDataDir  string
	removeRemote func(rigPath, name string) error
}

type doltRemote struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// NewDoltLocalOnlyRemoteCheck creates a per-rig local-only Dolt remote check.
func NewDoltLocalOnlyRemoteCheck(cityPath string, rig config.Rig, doltDataDir string) *DoltLocalOnlyRemoteCheck {
	if strings.TrimSpace(doltDataDir) == "" {
		doltDataDir = filepath.Join(cityPath, ".beads", "dolt")
	}
	return &DoltLocalOnlyRemoteCheck{
		cityPath:     cityPath,
		rig:          rig,
		doltDataDir:  doltDataDir,
		removeRemote: removeDoltRemote,
	}
}

// Name returns the check identifier ("rig:<name>:dolt-local-only-remote").
func (c *DoltLocalOnlyRemoteCheck) Name() string {
	return "rig:" + c.rig.Name + ":dolt-local-only-remote"
}

// Run executes the check.
func (c *DoltLocalOnlyRemoteCheck) Run(_ *CheckContext) *CheckResult {
	r := &CheckResult{Name: c.Name()}
	rigPath := c.normalizedRigPath()

	localOnly, err := c.localOnlyEnabled(rigPath)
	if err != nil {
		r.Status = StatusWarning
		r.Message = fmt.Sprintf("rig %q: cannot read dolt.local-only config: %v", c.rig.Name, err)
		return r
	}
	if !localOnly {
		r.Status = StatusOK
		r.Message = "dolt.local-only not enabled"
		return r
	}

	dbName, details := c.resolveDBName(rigPath)
	r.Details = append(r.Details, details...)
	remotes, err := localOnlyOffBoxRemotes(c.doltDataDir, dbName)
	if err != nil {
		r.Status = StatusWarning
		r.Message = fmt.Sprintf("rig %q: cannot inspect dolt remotes: %v", c.rig.Name, err)
		return r
	}
	if len(remotes) == 0 {
		r.Status = StatusOK
		r.Message = "dolt.local-only enabled; no off-box remotes registered"
		return r
	}

	names := remoteNames(remotes)
	r.Status = StatusWarning
	r.Message = fmt.Sprintf("rig %q: dolt.local-only forbids off-box remote(s): %s", c.rig.Name, strings.Join(names, ", "))
	r.FixHint = localOnlyRemoteFixHint(names)
	return r
}

// CanFix returns true. Fix is scoped by the explicit dolt.local-only flag.
func (c *DoltLocalOnlyRemoteCheck) CanFix() bool { return true }

// Fix removes off-box Dolt remotes only when dolt.local-only is explicitly true.
func (c *DoltLocalOnlyRemoteCheck) Fix(_ *CheckContext) error {
	rigPath := c.normalizedRigPath()
	localOnly, err := c.localOnlyEnabled(rigPath)
	if err != nil {
		return fmt.Errorf("read dolt.local-only config: %w", err)
	}
	if !localOnly {
		return nil
	}
	dbName, _ := c.resolveDBName(rigPath)
	remotes, err := localOnlyOffBoxRemotes(c.doltDataDir, dbName)
	if err != nil {
		return err
	}
	for _, remote := range remotes {
		if err := c.removeRemote(rigPath, remote.Name); err != nil {
			return fmt.Errorf("remove dolt remote %q: %w", remote.Name, err)
		}
	}
	return nil
}

func (c *DoltLocalOnlyRemoteCheck) normalizedRigPath() string {
	rigPath := c.rig.Path
	if !filepath.IsAbs(rigPath) {
		rigPath = filepath.Join(c.cityPath, rigPath)
	}
	return rigPath
}

func (c *DoltLocalOnlyRemoteCheck) localOnlyEnabled(rigPath string) (bool, error) {
	return contract.ReadDoltLocalOnly(fsys.OSFS{}, filepath.Join(rigPath, ".beads", "config.yaml"))
}

func (c *DoltLocalOnlyRemoteCheck) resolveDBName(rigPath string) (string, []string) {
	metadataPath := filepath.Join(rigPath, ".beads", "metadata.json")
	data, err := os.ReadFile(metadataPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return c.rig.Name, nil
		}
		return c.rig.Name, []string{fmt.Sprintf("read metadata.json: %v; using rig name %q", err, c.rig.Name)}
	}
	var meta struct {
		DoltDatabase string `json:"dolt_database"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return c.rig.Name, []string{fmt.Sprintf("parse metadata.json: %v; using rig name %q", err, c.rig.Name)}
	}
	if s := strings.TrimSpace(meta.DoltDatabase); s != "" {
		return s, nil
	}
	return c.rig.Name, nil
}

func localOnlyOffBoxRemotes(doltDataDir, dbName string) ([]doltRemote, error) {
	for _, statePath := range localOnlyRepoStatePaths(doltDataDir, dbName) {
		data, err := os.ReadFile(statePath)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, err
		}
		return parseLocalOnlyOffBoxRemotes(statePath, data)
	}
	return nil, nil
}

func localOnlyRepoStatePaths(doltDataDir, dbName string) []string {
	paths := []string{filepath.Join(doltDataDir, dbName, ".dolt", "repo_state.json")}
	direct := filepath.Join(doltDataDir, ".dolt", "repo_state.json")
	if direct != paths[0] {
		paths = append(paths, direct)
	}
	return paths
}

func parseLocalOnlyOffBoxRemotes(statePath string, data []byte) ([]doltRemote, error) {
	var state struct {
		Remotes map[string]doltRemote `json:"remotes"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("parse %s: %w", statePath, err)
	}
	remotes := make([]doltRemote, 0, len(state.Remotes))
	for key, remote := range state.Remotes {
		if strings.TrimSpace(remote.Name) == "" {
			remote.Name = key
		}
		if localOnlyRemoteIsOffBox(remote) {
			remotes = append(remotes, remote)
		}
	}
	sort.Slice(remotes, func(i, j int) bool {
		return remotes[i].Name < remotes[j].Name
	})
	return remotes, nil
}

func localOnlyRemoteIsOffBox(remote doltRemote) bool {
	name := strings.TrimSpace(remote.Name)
	if name == "origin" {
		return true
	}
	url := strings.ToLower(strings.TrimSpace(remote.URL))
	if url == "" {
		return false
	}
	for _, prefix := range []string{"http://", "https://", "git://", "git+http://", "git+https://", "ssh://"} {
		if strings.HasPrefix(url, prefix) {
			return true
		}
	}
	return strings.Contains(url, "@") && strings.Contains(url, ":")
}

func remoteNames(remotes []doltRemote) []string {
	names := make([]string, 0, len(remotes))
	for _, remote := range remotes {
		names = append(names, remote.Name)
	}
	return names
}

func localOnlyRemoteFixHint(names []string) string {
	commands := make([]string, 0, len(names))
	for _, name := range names {
		commands = append(commands, "bd dolt remote remove "+name)
	}
	return "gascity Dolt is local-only; run `gc doctor --fix` or remove manually: " + strings.Join(commands, "; ")
}

func removeDoltRemote(rigPath, name string) error {
	cmd := exec.Command("bd", "dolt", "remote", "remove", name)
	cmd.Dir = rigPath
	out, err := cmd.CombinedOutput()
	if err != nil {
		if msg := strings.TrimSpace(string(out)); msg != "" {
			return fmt.Errorf("%w: %s", err, msg)
		}
		return err
	}
	return nil
}
