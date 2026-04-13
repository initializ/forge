package packaging

import (
	"fmt"
	"strings"

	"github.com/initializ/forge/forge-core/types"
)

// GenerateDockerfile produces a Dockerfile with actual install commands for all resolved binaries.
// Returns (dockerfile content, warnings, error).
func GenerateDockerfile(manifest *BinManifest, cfg types.PackageConfig, alpine, slim bool) (string, []string, error) {
	if manifest == nil || len(manifest.Requirements) == 0 {
		return "", nil, nil
	}

	classifier, err := NewBinClassifier(cfg, slim, alpine)
	if err != nil {
		return "", nil, err
	}

	resolutions, warnings, err := classifier.Classify(manifest)
	if err != nil {
		return "", nil, fmt.Errorf("classifying binaries: %w", err)
	}

	baseImg := SelectBaseImage(resolutions, cfg.BaseImage, alpine)

	// Check if alpine was requested but blocked
	if alpine && !baseImg.IsAlpine {
		warnings = append(warnings, "Alpine base image requested but a binary requires Ubuntu; using debian:bookworm-slim")
	}

	var b strings.Builder

	// Companion stages for heavy (image-copy) binaries
	for _, r := range resolutions {
		if r.Method == MethodImageCopy && r.Image != "" {
			fmt.Fprintf(&b, "FROM %s AS bin-%s\n\n", r.Image, r.Name)
		}
	}

	// Base stage
	fmt.Fprintf(&b, "FROM %s AS bins\n", baseImg.Image)

	// Batch apt/apk packages
	var aptPkgs, apkPkgs []string
	hasDirectURL := false
	for _, r := range resolutions {
		switch r.Method {
		case MethodApt:
			aptPkgs = append(aptPkgs, r.Package)
		case MethodApk:
			apkPkgs = append(apkPkgs, r.Package)
		case MethodDirectURL:
			hasDirectURL = true
		}
	}

	// Ensure curl and ca-certificates are available for direct URL downloads
	if hasDirectURL && !alpine {
		hasCurl := false
		for _, p := range aptPkgs {
			if p == "curl" {
				hasCurl = true
				break
			}
		}
		if !hasCurl {
			aptPkgs = append([]string{"curl", "ca-certificates"}, aptPkgs...)
		}
	}
	if hasDirectURL && alpine {
		hasCurl := false
		for _, p := range apkPkgs {
			if p == "curl" {
				hasCurl = true
				break
			}
		}
		if !hasCurl {
			apkPkgs = append([]string{"curl", "ca-certificates"}, apkPkgs...)
		}
	}

	if len(aptPkgs) > 0 {
		fmt.Fprintf(&b, "RUN apt-get update && apt-get install -y --no-install-recommends \\\n")
		for i, pkg := range aptPkgs {
			if i < len(aptPkgs)-1 {
				fmt.Fprintf(&b, "    %s \\\n", pkg)
			} else {
				fmt.Fprintf(&b, "    %s \\\n", pkg)
			}
		}
		fmt.Fprintf(&b, "    && rm -rf /var/lib/apt/lists/*\n")
	}

	if len(apkPkgs) > 0 {
		fmt.Fprintf(&b, "RUN apk add --no-cache \\\n")
		for i, pkg := range apkPkgs {
			if i < len(apkPkgs)-1 {
				fmt.Fprintf(&b, "    %s \\\n", pkg)
			} else {
				fmt.Fprintf(&b, "    %s\n", pkg)
			}
		}
	}

	// Direct URL downloads
	for _, r := range resolutions {
		if r.Method == MethodDirectURL {
			dest := r.Dest
			if dest == "" {
				dest = "/usr/local/bin/" + r.Name
			}
			chmod := r.Chmod
			if chmod == "" {
				chmod = "0755"
			}
			fmt.Fprintf(&b, "RUN curl -fsSL %q -o %s && chmod %s %s\n", r.URL, dest, chmod, dest)
		}
	}

	// Custom RUN lines
	for _, r := range resolutions {
		if r.Method == MethodCustomRun {
			for _, line := range r.RunLines {
				fmt.Fprintf(&b, "RUN %s\n", line)
			}
		}
	}

	// Image-copy COPY instructions
	for _, r := range resolutions {
		if r.Method == MethodImageCopy {
			dest := r.Dest
			if dest == "" {
				dest = "/usr/local/bin/" + r.Name
			}
			fmt.Fprintf(&b, "COPY --from=bin-%s %s %s\n", r.Name, dest, dest)
		}
	}

	// Local file COPY instructions
	for _, r := range resolutions {
		if r.Method == MethodLocalFile {
			dest := r.Dest
			if dest == "" {
				dest = "/usr/local/bin/" + r.Name
			}
			chmod := r.Chmod
			if chmod == "" {
				chmod = "0755"
			}
			fmt.Fprintf(&b, "COPY .local-bins/%s %s\n", r.Name, dest)
			fmt.Fprintf(&b, "RUN chmod %s %s\n", chmod, dest)
		}
	}

	// PATH extensions
	var pathExts []string
	for _, r := range resolutions {
		if r.Dest != "" && r.Dest != "/usr/local/bin/"+r.Name {
			dir := r.Dest[:strings.LastIndex(r.Dest, "/")]
			if dir != "" && dir != "/usr/local/bin" && dir != "/usr/bin" {
				pathExts = append(pathExts, dir)
			}
		}
	}
	if len(pathExts) > 0 {
		fmt.Fprintf(&b, "ENV PATH=\"%s:$PATH\"\n", strings.Join(pathExts, ":"))
	}

	return b.String(), warnings, nil
}
