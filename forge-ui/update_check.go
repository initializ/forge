package forgeui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// updateInfo holds cached update check results.
type updateInfo struct {
	mu         sync.Mutex
	latest     string
	hasUpdate  bool
	checkedAt  time.Time
	checkErr   string
	cacheTTL   time.Duration
	currentVer string
	ghOwner    string
	ghRepo     string
}

func newUpdateChecker(currentVersion, owner, repo string) *updateInfo {
	return &updateInfo{
		currentVer: currentVersion,
		ghOwner:    owner,
		ghRepo:     repo,
		cacheTTL:   30 * time.Minute,
	}
}

// check fetches the latest release from GitHub and compares versions.
// Results are cached for cacheTTL.
func (u *updateInfo) check() (latestVersion string, hasUpdate bool, err string) {
	u.mu.Lock()
	defer u.mu.Unlock()

	// Return cached result if fresh.
	if !u.checkedAt.IsZero() && time.Since(u.checkedAt) < u.cacheTTL {
		return u.latest, u.hasUpdate, u.checkErr
	}

	u.checkedAt = time.Now()
	u.checkErr = ""
	u.hasUpdate = false

	// Skip check for dev builds.
	if u.currentVer == "" || u.currentVer == "dev" {
		return "", false, ""
	}

	latest, fetchErr := fetchLatestRelease(u.ghOwner, u.ghRepo)
	if fetchErr != nil {
		u.checkErr = fetchErr.Error()
		return "", false, u.checkErr
	}

	u.latest = latest
	u.hasUpdate = isNewer(u.currentVer, latest)
	return u.latest, u.hasUpdate, ""
}

// fetchLatestRelease queries the GitHub API for the latest release tag.
func fetchLatestRelease(owner, repo string) (string, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", owner, repo)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return "", fmt.Errorf("fetching release: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub API returned %d", resp.StatusCode)
	}

	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return "", fmt.Errorf("decoding response: %w", err)
	}

	return strings.TrimPrefix(release.TagName, "v"), nil
}

// isNewer returns true if latest is a higher semver than current.
// Simple numeric comparison of major.minor.patch components.
func isNewer(current, latest string) bool {
	cur := parseSemver(current)
	lat := parseSemver(latest)

	for i := 0; i < 3; i++ {
		if lat[i] > cur[i] {
			return true
		}
		if lat[i] < cur[i] {
			return false
		}
	}
	return false
}

// parseSemver extracts [major, minor, patch] from a version string.
// Non-numeric or missing parts default to 0.
func parseSemver(v string) [3]int {
	v = strings.TrimPrefix(v, "v")
	parts := strings.SplitN(v, ".", 3)
	var result [3]int
	for i, p := range parts {
		if i >= 3 {
			break
		}
		// Strip pre-release suffix (e.g. "1-rc1" → "1").
		if idx := strings.IndexAny(p, "-+"); idx >= 0 {
			p = p[:idx]
		}
		n := 0
		for _, c := range p {
			if c >= '0' && c <= '9' {
				n = n*10 + int(c-'0')
			} else {
				break
			}
		}
		result[i] = n
	}
	return result
}

// handleUpdateCheck returns update availability info.
func (s *UIServer) handleUpdateCheck(w http.ResponseWriter, _ *http.Request) {
	if s.updateChecker == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"has_update":      false,
			"current_version": s.cfg.Version,
		})
		return
	}

	latest, hasUpdate, checkErr := s.updateChecker.check()

	resp := map[string]any{
		"has_update":      hasUpdate,
		"current_version": s.cfg.Version,
		"latest_version":  latest,
	}
	if checkErr != "" {
		resp["error"] = checkErr
	}

	writeJSON(w, http.StatusOK, resp)
}
