package packaging

import (
	"fmt"
	"strings"

	"github.com/initializ/forge/forge-core/types"
)

// DockerfileFragments is the structured output of GenerateDockerfile.
// It separates the multi-stage pre-application chunks (companion
// image-copy stages + a shared bins stage for direct-URL / custom-RUN
// downloads) from the per-binary plumbing the application stage needs.
//
// Pre-issue #149 the generator returned a single string for "everything
// before the application stage" and the template emitted one blunt
// `COPY --from=bins /usr/local/bin/ /usr/local/bin/`. apt-installed
// binaries land at /usr/bin/ on Debian (not /usr/local/bin/) and have
// transitive lib/etc deps, so they could never survive the per-stage
// COPY — and the wholesale directory copy hid the issue from review.
//
// Post-fix:
//   - Apt/apk binaries are installed in the application stage directly,
//     so the package manager's dependency resolution pulls in the right
//     shared libs and config.
//   - Direct-URL / custom-RUN / local-file binaries still flow through a
//     shared `bins` stage (they need curl + a writable filesystem) and
//     are forwarded via per-binary explicit COPYs.
//   - Image-copy binaries skip the bins stage entirely and copy straight
//     from their dedicated `bin-<name>` companion stage to the app stage.
type DockerfileFragments struct {
	// PreAppStages is the Dockerfile chunk preceding the application
	// stage. May contain companion `FROM <upstream> AS bin-<name>`
	// stages for image-copy binaries plus a shared `bins` stage for
	// direct-URL / custom-RUN / local-file installs. Empty when no
	// binary needs a pre-app stage (e.g. an agent declaring only
	// apt-installable bins).
	PreAppStages string

	// RuntimeAptPackages are runtime apt packages the application
	// stage must install. Debian-only; nil on Alpine.
	RuntimeAptPackages []string

	// RuntimeApkPackages are runtime apk packages the application
	// stage must install. Alpine-only; nil on Debian.
	RuntimeApkPackages []string

	// BinCopies are formatted "COPY --from=<stage> <path> <path>"
	// lines emitted by the application stage. One per binary —
	// intent-explicit, no wholesale-directory copies. Empty when no
	// pre-app stage produced any binary.
	BinCopies []string

	// PathExtensions are PATH directories the application stage
	// exports for binaries installed at non-standard locations.
	PathExtensions []string
}

// HasPreAppStages reports whether any pre-application multi-stage
// content exists. Used by the template to decide whether to emit the
// "# --- Binary installation stages ---" header.
func (f DockerfileFragments) HasPreAppStages() bool {
	return strings.TrimSpace(f.PreAppStages) != ""
}

// GenerateDockerfile classifies the agent's binary requirements and
// returns the per-stage Dockerfile fragments. Callers compose the
// final Dockerfile by emitting PreAppStages, then the application
// stage with the BinCopies / RuntimeAptPackages / RuntimeApkPackages
// hooks honored.
//
// Returns (fragments, warnings, error). A nil/empty manifest returns
// zero-value fragments and no error.
func GenerateDockerfile(manifest *BinManifest, cfg types.PackageConfig, alpine, slim bool) (DockerfileFragments, []string, error) {
	if manifest == nil || len(manifest.Requirements) == 0 {
		return DockerfileFragments{}, nil, nil
	}

	classifier, err := NewBinClassifier(cfg, slim, alpine)
	if err != nil {
		return DockerfileFragments{}, nil, err
	}

	resolutions, warnings, err := classifier.Classify(manifest)
	if err != nil {
		return DockerfileFragments{}, nil, fmt.Errorf("classifying binaries: %w", err)
	}

	baseImg := SelectBaseImage(resolutions, cfg.BaseImage, alpine)
	if alpine && !baseImg.IsAlpine {
		warnings = append(warnings, "Alpine base image requested but a binary requires Ubuntu; using debian:bookworm-slim")
	}

	out := DockerfileFragments{}

	// Partition resolutions by install method.
	var (
		runtimeAptPkgs []string
		runtimeApkPkgs []string
		directURLs     []BinResolution
		customRuns     []BinResolution
		localFiles     []BinResolution
		imageCopies    []BinResolution
	)
	for _, r := range resolutions {
		switch r.Method {
		case MethodApt:
			runtimeAptPkgs = append(runtimeAptPkgs, r.Package)
		case MethodApk:
			runtimeApkPkgs = append(runtimeApkPkgs, r.Package)
		case MethodDirectURL:
			directURLs = append(directURLs, r)
		case MethodCustomRun:
			customRuns = append(customRuns, r)
		case MethodLocalFile:
			localFiles = append(localFiles, r)
		case MethodImageCopy:
			imageCopies = append(imageCopies, r)
		}
	}
	out.RuntimeAptPackages = runtimeAptPkgs
	out.RuntimeApkPackages = runtimeApkPkgs

	// Build the pre-application stages. Two chunks:
	//   (1) Companion `FROM <upstream> AS bin-<name>` stages for image-copy bins.
	//   (2) A shared `bins` stage that hosts curl-based direct-URL
	//       downloads + custom RUNs. Local-file copies do NOT need a
	//       bins stage — they're COPY-from-context, so they run in the
	//       application stage directly.
	var pre strings.Builder
	for _, r := range imageCopies {
		if r.Image != "" {
			fmt.Fprintf(&pre, "FROM %s AS bin-%s\n\n", r.Image, r.Name)
		}
	}

	needsSharedBinsStage := len(directURLs) > 0 || len(customRuns) > 0
	if needsSharedBinsStage {
		fmt.Fprintf(&pre, "FROM %s AS bins\n", baseImg.Image)

		// curl + ca-certificates are build-time helpers. Direct-URL
		// downloads use curl directly; custom-RUN scripts (e.g. the
		// `gh` install which pipes `curl | tar xz`) need them too.
		// They are scoped to the bins stage — the application stage
		// does not inherit them, so this install does not pollute the
		// runtime image.
		if alpine {
			pre.WriteString("RUN apk add --no-cache curl ca-certificates\n")
		} else {
			pre.WriteString("RUN apt-get update && apt-get install -y --no-install-recommends curl ca-certificates && rm -rf /var/lib/apt/lists/*\n")
		}

		for _, r := range directURLs {
			dest := r.Dest
			if dest == "" {
				dest = "/usr/local/bin/" + r.Name
			}
			chmod := r.Chmod
			if chmod == "" {
				chmod = "0755"
			}
			fmt.Fprintf(&pre, "RUN curl -fsSL %q -o %s && chmod %s %s\n", r.URL, dest, chmod, dest)
		}
		for _, r := range customRuns {
			for _, line := range r.RunLines {
				fmt.Fprintf(&pre, "RUN %s\n", line)
			}
		}
	}
	out.PreAppStages = pre.String()

	// Build the per-binary COPY lines for the application stage.
	//   - Direct-URL / custom-RUN binaries come from the shared `bins` stage.
	//   - Image-copy binaries come straight from their `bin-<name>` companion
	//     stage, skipping the bins-stage intermediate hop.
	//   - Local-file binaries are COPY-from-build-context, emitted in the app
	//     stage (no --from).
	var copies []string
	for _, r := range directURLs {
		dest := r.Dest
		if dest == "" {
			dest = "/usr/local/bin/" + r.Name
		}
		copies = append(copies, fmt.Sprintf("COPY --from=bins %s %s", dest, dest))
	}
	for _, r := range customRuns {
		dest := r.Dest
		if dest == "" {
			dest = "/usr/local/bin/" + r.Name
		}
		copies = append(copies, fmt.Sprintf("COPY --from=bins %s %s", dest, dest))
	}
	for _, r := range imageCopies {
		dest := r.Dest
		if dest == "" {
			dest = "/usr/local/bin/" + r.Name
		}
		copies = append(copies, fmt.Sprintf("COPY --from=bin-%s %s %s", r.Name, dest, dest))
	}
	for _, r := range localFiles {
		dest := r.Dest
		if dest == "" {
			dest = "/usr/local/bin/" + r.Name
		}
		chmod := r.Chmod
		if chmod == "" {
			chmod = "0755"
		}
		// Local files use COPY-from-build-context; emit the COPY +
		// chmod pair so the binary lands executable.
		copies = append(copies, fmt.Sprintf("COPY .local-bins/%s %s", r.Name, dest))
		copies = append(copies, fmt.Sprintf("RUN chmod %s %s", chmod, dest))
	}
	out.BinCopies = copies

	// PATH extensions for binaries installed at non-standard locations.
	var pathExts []string
	for _, r := range resolutions {
		if r.Dest != "" && r.Dest != "/usr/local/bin/"+r.Name {
			idx := strings.LastIndex(r.Dest, "/")
			if idx <= 0 {
				continue
			}
			dir := r.Dest[:idx]
			if dir != "/usr/local/bin" && dir != "/usr/bin" {
				pathExts = append(pathExts, dir)
			}
		}
	}
	out.PathExtensions = pathExts

	return out, warnings, nil
}
