package packaging

import (
	"fmt"

	"github.com/initializ/forge/forge-core/types"
	"github.com/initializ/forge/forge-skills/contract"
	"github.com/initializ/forge/forge-skills/registry"
)

// InstallMethod describes how a binary will be installed.
type InstallMethod string

const (
	MethodApt       InstallMethod = "apt"
	MethodApk       InstallMethod = "apk"
	MethodDirectURL InstallMethod = "direct-url"
	MethodCustomRun InstallMethod = "custom-run"
	MethodImageCopy InstallMethod = "image-copy"
	MethodLocalFile InstallMethod = "local-file" // local binary copied into build context
	MethodSkip      InstallMethod = "skip"       // dependency already provided by another binary
)

// BinResolution is the resolved install plan for a single binary.
type BinResolution struct {
	Name           string
	Method         InstallMethod
	Package        string   // apt/apk package name
	URL            string   // expanded URL for direct download
	Dest           string   // install destination path
	Chmod          string   // permission bits
	RunLines       []string // custom RUN commands
	Image          string   // companion image for image-copy
	LocalPath      string   // host file path for local-file method
	Version        string   // resolved version
	Optional       bool
	RequiresUbuntu bool
	RequiresFirst  []string // dependencies
}

// BinClassifier resolves binary requirements into install plans.
type BinClassifier struct {
	cfg    types.PackageConfig
	slim   bool
	alpine bool
	reg    *registry.ImageRegistry
}

// NewBinClassifier creates a classifier with the given config.
func NewBinClassifier(cfg types.PackageConfig, slim, alpine bool) (*BinClassifier, error) {
	reg, err := registry.Default()
	if err != nil {
		return nil, fmt.Errorf("loading image registry: %w", err)
	}
	return &BinClassifier{cfg: cfg, slim: slim, alpine: alpine, reg: reg}, nil
}

// Classify resolves all bin requirements into install plans.
// Returns (resolutions, warnings, error).
func (c *BinClassifier) Classify(manifest *BinManifest) ([]BinResolution, []string, error) {
	var resolutions []BinResolution
	var warnings []string
	seen := make(map[string]bool)

	for _, req := range manifest.Requirements {
		if seen[req.Name] {
			continue
		}
		seen[req.Name] = true

		res, w, err := c.resolveOne(req)
		if err != nil {
			return nil, nil, fmt.Errorf("resolving %q: %w", req.Name, err)
		}
		if w != "" {
			warnings = append(warnings, w)
		}
		resolutions = append(resolutions, res)
	}

	// Topological sort for dependency ordering
	sorted, err := topoSort(resolutions)
	if err != nil {
		return nil, nil, err
	}

	return sorted, warnings, nil
}

// resolveOne resolves a single binary using the priority chain:
// 0. Local file override → 1. Skill-local override → 2. forge.yaml override → 3. Registry → 4. Best-effort apt
func (c *BinClassifier) resolveOne(req contract.BinRequirement) (BinResolution, string, error) {
	res := BinResolution{
		Name:     req.Name,
		Optional: req.Optional,
		Dest:     req.Dest,
		Chmod:    req.Chmod,
	}

	// 0. Local file override (highest priority)
	if override, ok := c.cfg.BinOverrides[req.Name]; ok && override.LocalPath != "" {
		return c.applyLocalOverride(res, override), "", nil
	}

	// 1. Skill-local override (fields set directly on BinRequirement)
	if req.DirectURL != "" || len(req.CustomLines) > 0 || req.AptPackage != "" || req.ApkPackage != "" {
		return c.applySkillOverride(res, req)
	}

	// 2. forge.yaml override
	if override, ok := c.cfg.BinOverrides[req.Name]; ok {
		return c.applyConfigOverride(res, override, req.Version)
	}

	// 3. Registry lookup
	if entry, ok := c.reg.Lookup(req.Name); ok {
		return c.applyRegistry(res, entry, req.Version)
	}

	// 4. Best-effort: assume apt package name matches binary name
	warning := fmt.Sprintf("binary %q not found in registry; assuming apt package %q", req.Name, req.Name)
	if c.alpine {
		res.Method = MethodApk
		res.Package = req.Name
	} else {
		res.Method = MethodApt
		res.Package = req.Name
	}
	return res, warning, nil
}

func (c *BinClassifier) applyLocalOverride(res BinResolution, override types.BinOverride) BinResolution {
	res.Method = MethodLocalFile
	res.LocalPath = override.LocalPath
	if override.Dest != "" {
		res.Dest = override.Dest
	} else if res.Dest == "" {
		res.Dest = "/usr/local/bin/" + res.Name
	}
	if override.Chmod != "" {
		res.Chmod = override.Chmod
	} else if res.Chmod == "" {
		res.Chmod = "0755"
	}
	return res
}

func (c *BinClassifier) applySkillOverride(res BinResolution, req contract.BinRequirement) (BinResolution, string, error) {
	if len(req.CustomLines) > 0 {
		res.Method = MethodCustomRun
		res.RunLines = expandRunLines(req.CustomLines, req.Version)
		return res, "", nil
	}
	if req.DirectURL != "" {
		expanded, err := registry.ExpandTemplate(req.DirectURL, req.Version)
		if err != nil {
			return res, "", err
		}
		res.Method = MethodDirectURL
		res.URL = expanded
		if res.Dest == "" {
			res.Dest = "/usr/local/bin/" + req.Name
		}
		if res.Chmod == "" {
			res.Chmod = "0755"
		}
		return res, "", nil
	}
	if c.alpine && req.ApkPackage != "" {
		res.Method = MethodApk
		res.Package = req.ApkPackage
		return res, "", nil
	}
	if req.AptPackage != "" {
		res.Method = MethodApt
		res.Package = req.AptPackage
		return res, "", nil
	}
	// Fallback for alpine when only apt specified
	if req.AptPackage != "" {
		res.Method = MethodApt
		res.Package = req.AptPackage
	}
	return res, "", nil
}

func (c *BinClassifier) applyConfigOverride(res BinResolution, override types.BinOverride, version string) (BinResolution, string, error) {
	if len(override.CustomLines) > 0 {
		res.Method = MethodCustomRun
		res.RunLines = expandRunLines(override.CustomLines, version)
		return res, "", nil
	}
	if override.DirectURL != "" {
		expanded, err := registry.ExpandTemplate(override.DirectURL, version)
		if err != nil {
			return res, "", err
		}
		res.Method = MethodDirectURL
		res.URL = expanded
		if override.Dest != "" {
			res.Dest = override.Dest
		} else if res.Dest == "" {
			res.Dest = "/usr/local/bin/" + res.Name
		}
		if override.Chmod != "" {
			res.Chmod = override.Chmod
		} else if res.Chmod == "" {
			res.Chmod = "0755"
		}
		return res, "", nil
	}
	if c.alpine && override.ApkPackage != "" {
		res.Method = MethodApk
		res.Package = override.ApkPackage
		return res, "", nil
	}
	if override.AptPackage != "" {
		res.Method = MethodApt
		res.Package = override.AptPackage
		return res, "", nil
	}
	return res, "", nil
}

func (c *BinClassifier) applyRegistry(res BinResolution, entry registry.RegistryEntry, reqVersion string) (BinResolution, string, error) {
	version := entry.ResolveVersion(reqVersion)
	res.Version = version
	res.RequiresUbuntu = entry.RequiresUbuntu
	res.RequiresFirst = entry.RequiresFirst

	// Heavy binaries use companion image
	if entry.Heavy && entry.Image != "" {
		expanded, err := registry.ExpandTemplate(entry.Image, version)
		if err != nil {
			return res, "", err
		}
		res.Method = MethodImageCopy
		res.Image = expanded
		return res, "", nil
	}

	// Custom run lines
	if len(entry.Run) > 0 {
		res.Method = MethodCustomRun
		res.RunLines = expandRunLines(entry.Run, version)
		return res, "", nil
	}

	// Direct URL
	if entry.URL != "" && !c.alpine {
		expanded, err := registry.ExpandTemplate(entry.URL, version)
		if err != nil {
			return res, "", err
		}
		res.Method = MethodDirectURL
		res.URL = expanded
		if entry.Dest != "" {
			res.Dest = entry.Dest
		} else if res.Dest == "" {
			res.Dest = "/usr/local/bin/" + res.Name
		}
		if entry.Chmod != "" {
			res.Chmod = entry.Chmod
		} else if res.Chmod == "" {
			res.Chmod = "0755"
		}
		return res, "", nil
	}

	// Package manager
	if c.alpine {
		if entry.Apk != "" {
			res.Method = MethodApk
			res.Package = entry.Apk
		} else if entry.Apt != "" {
			// Alpine but only apt available — warn
			res.Method = MethodApt
			res.Package = entry.Apt
			return res, fmt.Sprintf("binary %q has no apk package; falling back to apt (may fail on Alpine)", res.Name), nil
		} else {
			res.Method = MethodApt
			res.Package = res.Name
			return res, fmt.Sprintf("binary %q has no package info; assuming apt package %q", res.Name, res.Name), nil
		}
	} else {
		if entry.Apt != "" {
			res.Method = MethodApt
			res.Package = entry.Apt
		} else {
			res.Method = MethodApt
			res.Package = res.Name
		}
	}

	return res, "", nil
}

func expandRunLines(lines []string, version string) []string {
	if version == "" {
		return lines
	}
	expanded := make([]string, len(lines))
	for i, line := range lines {
		result, err := registry.ExpandTemplate(line, version)
		if err != nil {
			expanded[i] = line // keep original on error
		} else {
			expanded[i] = result
		}
	}
	return expanded
}

// topoSort performs topological sort using Kahn's algorithm based on RequiresFirst.
func topoSort(resolutions []BinResolution) ([]BinResolution, error) {
	if len(resolutions) <= 1 {
		return resolutions, nil
	}

	byName := make(map[string]int)
	for i, r := range resolutions {
		byName[r.Name] = i
	}

	// Build in-degree counts and adjacency
	inDegree := make(map[int]int)
	dependents := make(map[int][]int) // dep index → list of dependent indices

	for i, r := range resolutions {
		inDegree[i] = 0
		for _, dep := range r.RequiresFirst {
			if depIdx, ok := byName[dep]; ok {
				dependents[depIdx] = append(dependents[depIdx], i)
				inDegree[i]++
			}
		}
	}

	// Find all nodes with no incoming edges
	var queue []int
	for i := range resolutions {
		if inDegree[i] == 0 {
			queue = append(queue, i)
		}
	}

	var sorted []BinResolution
	for len(queue) > 0 {
		idx := queue[0]
		queue = queue[1:]
		sorted = append(sorted, resolutions[idx])

		for _, depIdx := range dependents[idx] {
			inDegree[depIdx]--
			if inDegree[depIdx] == 0 {
				queue = append(queue, depIdx)
			}
		}
	}

	if len(sorted) != len(resolutions) {
		return nil, fmt.Errorf("circular dependency detected in bin requirements")
	}

	return sorted, nil
}
