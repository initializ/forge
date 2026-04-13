package packaging

// BaseImage holds the selected base image information.
type BaseImage struct {
	Image    string // e.g. "debian:bookworm-slim", "alpine:3.20"
	IsAlpine bool
}

// SelectBaseImage chooses the appropriate base image based on resolved binaries and config.
// Priority: cfg.BaseImage → alpine flag → RequiresUbuntu detection → default debian:bookworm-slim.
func SelectBaseImage(resolutions []BinResolution, baseImage string, alpine bool) BaseImage {
	// 1. Explicit base image from config
	if baseImage != "" {
		return BaseImage{Image: baseImage, IsAlpine: alpine}
	}

	// 2. Check if any binary requires Ubuntu (incompatible with Alpine)
	requiresUbuntu := false
	for _, r := range resolutions {
		if r.RequiresUbuntu {
			requiresUbuntu = true
			break
		}
	}

	// 3. Alpine requested and possible
	if alpine && !requiresUbuntu {
		return BaseImage{Image: "alpine:3.20", IsAlpine: true}
	}

	// 4. Alpine requested but blocked
	if alpine && requiresUbuntu {
		// Fall through to debian — caller should warn
		return BaseImage{Image: "debian:bookworm-slim", IsAlpine: false}
	}

	// 5. Default
	return BaseImage{Image: "debian:bookworm-slim", IsAlpine: false}
}
