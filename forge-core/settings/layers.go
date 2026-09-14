package settings

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
)

// Layer names, lowest → highest precedence. Managed is highest and, for the
// AvailableModels allowlist, authoritative (a lock). Mirrors Claude Code's
// precedence: managed > CLI > project-local > project > user.
const (
	LayerUser         = "user"
	LayerProject      = "project"
	LayerProjectLocal = "project-local"
	LayerCLI          = "cli"
	LayerManaged      = "managed"
)

// EnvUserSettings redirects the USER layer's file (test isolation /
// XDG-style installs). Redirecting the user's own lowest-precedence layer is
// harmless — managed settings still override it — so this env is honored in
// production.
//
// There is deliberately NO env override for the MANAGED layer. Managed
// settings are the enterprise tier and MUST NOT be developer-overridable: the
// whole point of the MDM/managed source is that a developer running the
// shipped binary cannot redirect, replace, or drop it. Production reads only
// the fixed OS system path (managedDir), whose tamper-resistance is the OS file
// permissions on a managed machine (root-owned; the same model as Claude Code's
// fixed managed-settings.json path). Tests inject a managed dir via
// SetManagedDirForTest — an in-code hook reachable only by recompiling, never
// by env/flag/config at runtime.
const EnvUserSettings = "FORGE_USER_SETTINGS"

// managedDirOverride is a TEST-ONLY injection point for the managed system
// directory, set via SetManagedDirForTest. It is never populated from any
// runtime input (env, flag, config), so it cannot be used to override managed
// settings on the shipped binary.
var managedDirOverride string

// SetManagedDirForTest points the managed layer at dir (containing
// managed-settings.json and an optional managed-settings.d/) and returns a
// restore func. TEST-ONLY: nothing in the production code path calls it, so a
// developer cannot use it to override managed settings without recompiling.
func SetManagedDirForTest(dir string) (restore func()) {
	prev := managedDirOverride
	managedDirOverride = dir
	return func() { managedDirOverride = prev }
}

// Layer is one loaded settings source. Path is the file it came from (or the
// managed primary file); ManagedLock is true only for the managed layer when
// it set AvailableModels (authoritative allowlist).
type Layer struct {
	Source      string
	Path        string
	Settings    Settings
	ManagedLock bool
}

// LoadOptions parameterizes discovery. CLISettingsPath is the --settings file
// (empty = none); WorkingDir roots the project layers (empty = os.Getwd).
type LoadOptions struct {
	CLISettingsPath string
	WorkingDir      string
}

// UserSettingsPath returns ~/.forge/settings.json (or the EnvUserSettings
// override). Empty when there is no home dir.
func UserSettingsPath() string {
	if p := os.Getenv(EnvUserSettings); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".forge", "settings.json")
}

// ManagedSettingsPath returns the fixed OS system-directory
// managed-settings.json, mirroring Claude Code's locations. There is no
// runtime override — see EnvUserSettings's comment.
func ManagedSettingsPath() string {
	return filepath.Join(managedDir(), "managed-settings.json")
}

// managedDir is the OS system directory holding managed-settings.json and the
// managed-settings.d/ drop-in directory. The paths are FIXED (not derived from
// user-settable env like %ProgramFiles%) so a developer cannot redirect the
// managed layer at runtime; tamper-resistance is the OS file permissions on a
// managed machine. managedDirOverride is honored ONLY for tests.
func managedDir() string {
	if managedDirOverride != "" {
		return managedDirOverride
	}
	switch runtime.GOOS {
	case "darwin":
		return "/Library/Application Support/forge"
	case "windows":
		return `C:\Program Files\forge`
	default: // linux, wsl, others
		return "/etc/forge"
	}
}

// LoadAllLayers discovers and loads every present settings layer, returning
// them in precedence order (lowest → highest): user, project, project-local,
// CLI, managed. Absent/empty files are omitted. A malformed file at any layer
// is a hard error — a typo must fail loudly, never silently drop a layer
// (matching LoadAllPolicyLayers).
func LoadAllLayers(opts LoadOptions) ([]Layer, error) {
	wd := opts.WorkingDir
	if wd == "" {
		if cwd, err := os.Getwd(); err == nil {
			wd = cwd
		}
	}

	var out []Layer
	add := func(source, path string) error {
		if path == "" {
			return nil
		}
		s, present, err := loadFile(path)
		if err != nil {
			return fmt.Errorf("loading %s settings (%s): %w", source, path, err)
		}
		if present {
			out = append(out, Layer{Source: source, Path: path, Settings: s})
		}
		return nil
	}

	if err := add(LayerUser, UserSettingsPath()); err != nil {
		return nil, err
	}
	if wd != "" {
		if err := add(LayerProject, filepath.Join(wd, ".forge", "settings.json")); err != nil {
			return nil, err
		}
		if err := add(LayerProjectLocal, filepath.Join(wd, ".forge", "settings.local.json")); err != nil {
			return nil, err
		}
	}
	if err := add(LayerCLI, opts.CLISettingsPath); err != nil {
		return nil, err
	}

	managed, err := loadManagedLayer()
	if err != nil {
		return nil, err
	}
	if managed != nil {
		out = append(out, *managed)
	}
	return out, nil
}

// loadManagedLayer merges managed-settings.json with every *.json in the
// sibling managed-settings.d/ directory (alphabetical, primary first), all as
// a single managed layer. Returns nil when none is present. ManagedLock is set
// when the merged managed settings declare AvailableModels (authoritative).
func loadManagedLayer() (*Layer, error) {
	primary := ManagedSettingsPath()
	dropinDir := filepath.Join(filepath.Dir(primary), "managed-settings.d")

	var files []string
	if _, err := os.Stat(primary); err == nil {
		files = append(files, primary)
	}
	if entries, err := os.ReadDir(dropinDir); err == nil {
		var dropins []string
		for _, e := range entries {
			if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
				continue
			}
			dropins = append(dropins, filepath.Join(dropinDir, e.Name()))
		}
		sort.Strings(dropins) // numeric-prefix ordering, like Claude Code
		files = append(files, dropins...)
	}
	if len(files) == 0 {
		return nil, nil
	}

	var merged Settings
	lockedAvailable := false
	for _, f := range files {
		s, present, err := loadFile(f)
		if err != nil {
			return nil, fmt.Errorf("loading managed settings (%s): %w", f, err)
		}
		if !present {
			continue
		}
		// A non-empty managed available_models makes it the authoritative
		// settings-layer allowlist (Resolve replaces the union with it). An
		// empty/absent list is "unset", NOT "lock to zero models": []string
		// cannot distinguish JSON null/absent from [], and locking to zero
		// models is degenerate (an org permitting nothing would not deploy the
		// agent). Lock therefore requires >= 1 entry.
		if len(s.Models.AvailableModels) > 0 {
			lockedAvailable = true
		}
		merged = merge(merged, s)
	}
	return &Layer{Source: LayerManaged, Path: primary, Settings: merged, ManagedLock: lockedAvailable}, nil
}

// loadFile reads a settings JSON file. Returns present=false (no error) when
// the file is absent or empty — the optional-mount semantics the policy loader
// uses. Unknown fields are rejected so a typo'd key fails loudly.
func loadFile(path string) (s Settings, present bool, err error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Settings{}, false, nil
		}
		return Settings{}, false, err
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return Settings{}, false, nil
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return Settings{}, false, fmt.Errorf("parse: %w", err)
	}
	return s, true, nil
}

// Resolve folds the layers (lowest → highest precedence) into the effective
// Settings. The managed layer's AvailableModels, when set, REPLACES the merged
// union rather than adding to it — so no lower layer can widen the org's
// allowlist. Because the managed layer loads from a fixed OS path with no
// runtime override (see managedDir / EnvUserSettings comment), a developer
// cannot redirect it either; the allowlist is bounded by the OS file
// permissions on the managed path. Platform policy (server-side) is the
// defense-in-depth forbidden-model enforcement.
func Resolve(layers []Layer) Settings {
	var out Settings
	managedAvailable := []string(nil)
	managedLocked := false
	for _, l := range layers {
		out = merge(out, l.Settings)
		if l.Source == LayerManaged && l.ManagedLock {
			managedLocked = true
			managedAvailable = l.Settings.Models.AvailableModels
		}
	}
	if managedLocked {
		out.Models.AvailableModels = managedAvailable
	}
	return out
}

// ManagedLayer returns the managed layer from a loaded set, or nil when no
// managed settings are present. The login gate (#455) uses this: only a
// MANAGED-layer gateway helper arms the auto-login gate — a user-layer helper
// stays on the manual `forge auth` path.
func ManagedLayer(layers []Layer) *Layer {
	for i := range layers {
		if layers[i].Source == LayerManaged {
			return &layers[i]
		}
	}
	return nil
}

// TrustedGatewayLayers drops the checked-in project layer (.forge/settings.json)
// from a loaded set. That file ships inside a cloned repo, so it MUST NOT be
// able to configure a model gateway: a gateway carries an api_key_helper (an
// external command forge would exec — host RCE via `forge auth login`) and a
// base_url/auth_scheme (an endpoint redirect that would send the native
// provider key to an attacker on plain `forge run`). The gateway overlay and
// `forge auth login` resolve from these TRUSTED layers only — user,
// project-LOCAL (.forge/settings.local.json, gitignored), CLI (--settings, an
// explicit dev choice), and managed. Other settings (channels, skills,
// models.default) legitimately honor the checked-in project layer; only the
// gateway is trust-sensitive, so this filter is applied narrowly at gateway
// resolution, not globally. See PR #464 review (HIGH #1/#2).
func TrustedGatewayLayers(layers []Layer) []Layer {
	out := make([]Layer, 0, len(layers))
	for _, l := range layers {
		if l.Source == LayerProject {
			continue
		}
		out = append(out, l)
	}
	return out
}

// Load discovers all layers and returns the resolved effective settings. The
// per-layer detail (for `forge settings` provenance) comes from LoadAllLayers.
func Load(opts LoadOptions) (Settings, error) {
	layers, err := LoadAllLayers(opts)
	if err != nil {
		return Settings{}, err
	}
	return Resolve(layers), nil
}
