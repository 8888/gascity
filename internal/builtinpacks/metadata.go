package builtinpacks

import (
	"fmt"
	"regexp"
	"runtime/debug"
	"strings"
)

var goPseudoVersionSuffixes = []*regexp.Regexp{
	regexp.MustCompile(`^(.*)\.0\.\d{14}-[0-9a-f]{12,}$`),
	regexp.MustCompile(`^(.*)-0\.\d{14}-[0-9a-f]{12,}$`),
	regexp.MustCompile(`^(.*)-\d{14}-[0-9a-f]{12,}$`),
}

// CurrentResolution returns the version and commit used for synthetic bundled
// pack lock entries.
func CurrentResolution() (version, commit string, err error) {
	info, ok := debug.ReadBuildInfo()
	version = "dev"
	commit = "unknown"
	if ok && info != nil {
		if info.Main.Version != "" && info.Main.Version != "(devel)" {
			version = normalizeBuildVersion(info.Main.Version)
		}
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				if setting.Value != "" {
					commit = setting.Value
				}
			case "vcs.modified":
				if setting.Value == "true" && commit != "unknown" {
					commit += "-dirty"
				}
			}
		}
	}
	if commit == "unknown" {
		hash, hashErr := SyntheticContentHash()
		if hashErr != nil {
			return "", "", hashErr
		}
		commit = "synthetic-" + strings.TrimPrefix(hash, "sha256:")[:12]
	}
	if version == "" || version == "dev" {
		version = "sha:" + commit
	}
	return version, commit, nil
}

func normalizeBuildVersion(v string) string {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "v")
	if v == "" || v == "(devel)" || v == "0.0.0" {
		return "dev"
	}
	if i := strings.IndexByte(v, '+'); i >= 0 {
		v = v[:i]
	}
	for _, re := range goPseudoVersionSuffixes {
		if m := re.FindStringSubmatch(v); len(m) == 2 {
			v = m[1]
			break
		}
	}
	if v == "" {
		return "dev"
	}
	return v
}

// ResolveCurrent applies an optional pack import version constraint to the
// current bundled pack resolution.
func ResolveCurrent(constraint string, match func(version, constraint string) bool) (version, commit string, err error) {
	if strings.HasPrefix(constraint, "sha:") {
		commit := strings.TrimPrefix(constraint, "sha:")
		if commit == "" {
			return "", "", fmt.Errorf("empty sha constraint")
		}
		return constraint, commit, nil
	}
	version, commit, err = CurrentResolution()
	if err != nil {
		return "", "", err
	}
	if constraint != "" && !match(version, constraint) {
		return "", "", fmt.Errorf("bundled pack version %q does not satisfy constraint %q", version, constraint)
	}
	return version, commit, nil
}
