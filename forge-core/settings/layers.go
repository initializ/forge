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

// Env overrides (test isolation / non-standard installs), mirroring the policy
// layers' FORGE_SYSTEM_POLICY. EnvManagedSettings points at the managed file;
// its sibling managed-settings.d/ directory is derived from it.
//
// NOTE: EnvManagedSettings is honored unconditionally, so the managed layer's
// SOURCE is user-redirectable — settings are the developer surface, an org
// *default/preference*, not a tamper-proof boundary. Do not rely on a managed
// available_models list as a hard control: non-overridable forbidden-model
// enforcement lives in platform policy (server-side, control-plane injected;
// see forge-core/security/platform_policy_layers.go). This mirrors the policy
// loader, whose FORGE_SYSTEM_POLICY is likewise redirectable — the authoritative
// enforcement in both cases is the control plane, not a local file.
const (
	EnvManagedSettings = "FORGE_MANAGED_SETTINGS"
	EnvUserSettings    = "FORGE_USER_SETTINGS"
)

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

// ManagedSettingsPath returns the OS system-directory managed-settings.json
// (or the EnvManagedSettings override), mirroring Claude Code's locations.
func ManagedSettingsPath() string {
	if p := os.Getenv(EnvManagedSettings); p != "" {
		return p
	}
	return filepath.Join(managedDir(), "managed-settings.json")
}

// managedDir is the OS system directory holding managed-settings.json and the
// managed-settings.d/ drop-in directory.
func managedDir() string {
	switch runtime.GOOS {
	case "darwin":
		return "/Library/Application Support/forge"
	case "windows":
		base := os.Getenv("ProgramFiles")
		if base == "" {
			base = `C:\Program Files`
		}
		return filepath.Join(base, "forge")
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
// union rather than adding to it — so no lower SETTINGS layer can widen the
// org's allowlist. This is the authoritative allowlist among settings layers,
// an org default/preference — NOT a tamper-proof control (the managed source is
// redirectable via FORGE_MANAGED_SETTINGS; see that const). Non-overridable
// forbidden-model enforcement is platform policy's job (server-side).
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

// Load discovers all layers and returns the resolved effective settings. The
// per-layer detail (for `forge settings` provenance) comes from LoadAllLayers.
func Load(opts LoadOptions) (Settings, error) {
	layers, err := LoadAllLayers(opts)
	if err != nil {
		return Settings{}, err
	}
	return Resolve(layers), nil
}
