// Package browser implements the opt-in browser tool family (issue #94):
// a chromedp-driven headless Chromium whose traffic is forced through the
// agent's EgressProxy, exposed to the LLM as token-optimized tools that
// exchange indexed page digests instead of raw HTML.
package browser

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/initializ/forge/forge-core/security"
)

const (
	defaultNavTimeout    = 45 * time.Second
	defaultActionTimeout = 30 * time.Second
)

// Config holds everything the Manager needs to launch and drive Chromium.
type Config struct {
	// BinaryPath is the resolved absolute path to a Chromium-compatible binary.
	BinaryPath string
	// Headless launches with --headless=new when true.
	Headless bool
	// ProxyURL is the EgressProxy address (http://127.0.0.1:<port>). Required:
	// the manager refuses to launch an unproxied browser.
	ProxyURL string
	// WorkDir is the agent working directory; the throwaway browser profile
	// and screenshot fallback directory live under it.
	WorkDir string
	// AllowSensitiveFill permits browser_fill on password/payment fields.
	AllowSensitiveFill bool

	NavTimeout    time.Duration
	ActionTimeout time.Duration
}

// Manager owns at most one Chromium process per agent, launched lazily on the
// first tool call and stopped by the runner on shutdown. All tool executions
// are serialized: the LLM drives a single logical tab.
type Manager struct {
	cfg Config

	mu          sync.Mutex
	allocCtx    context.Context
	allocCancel context.CancelFunc
	tabCtx      context.Context
	tabCancel   context.CancelFunc
	profileDir  string

	// gen is the digest generation counter; bumped on every snapshot so
	// interaction tools can detect stale element indices.
	gen int64
	// shots numbers default screenshot filenames.
	shots int64
}

// nextShot returns a monotonically increasing screenshot number.
func (m *Manager) nextShot() int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.shots++
	return m.shots
}

// NewManager validates cfg and returns an unlaunched Manager.
func NewManager(cfg Config) (*Manager, error) {
	if cfg.BinaryPath == "" {
		return nil, fmt.Errorf("browser: BinaryPath is required")
	}
	if cfg.ProxyURL == "" {
		return nil, fmt.Errorf("browser: ProxyURL is required; refusing to run an unproxied browser")
	}
	if cfg.WorkDir == "" {
		return nil, fmt.Errorf("browser: WorkDir is required")
	}
	if cfg.NavTimeout <= 0 {
		cfg.NavTimeout = defaultNavTimeout
	}
	if cfg.ActionTimeout <= 0 {
		cfg.ActionTimeout = defaultActionTimeout
	}
	return &Manager{cfg: cfg}, nil
}

// allocatorOptions builds the Chromium launch flags. Notable choices:
//   - --proxy-bypass-list=<-loopback> forces even localhost traffic through
//     the egress proxy (Chrome bypasses proxies for loopback by default,
//     which would be an egress escape hatch).
//   - the profile is a throwaway directory so no cookies/sessions persist
//     across runs.
func (m *Manager) allocatorOptions() []chromedp.ExecAllocatorOption {
	opts := []chromedp.ExecAllocatorOption{
		chromedp.ExecPath(m.cfg.BinaryPath),
		chromedp.NoFirstRun,
		chromedp.NoDefaultBrowserCheck,
		chromedp.UserDataDir(m.profileDir),
		chromedp.ProxyServer(m.cfg.ProxyURL),
		chromedp.Flag("proxy-bypass-list", "<-loopback>"),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("disable-extensions", true),
		chromedp.Flag("disable-background-networking", true),
		chromedp.Flag("disable-sync", true),
		chromedp.Flag("disable-default-apps", true),
		chromedp.Flag("mute-audio", true),
		// Suppress Chrome phone-home traffic (safebrowsing, component updates,
		// field trials). It would be blocked by the proxy anyway, but it
		// pollutes the egress audit log.
		chromedp.Flag("disable-component-update", true),
		chromedp.Flag("disable-client-side-phishing-detection", true),
		chromedp.Flag("disable-domain-reliability", true),
		chromedp.Flag("no-pings", true),
		chromedp.Flag("safebrowsing-disable-auto-update", true),
		chromedp.Flag("disable-features", "SafeBrowsing,OptimizationHints,MediaRouter,Translate,InterestFeedContentSuggestions"),
	}
	if m.cfg.Headless {
		opts = append(opts, chromedp.Flag("headless", "new"))
	}
	if security.InContainer() {
		// Chromium's sandbox needs privileges containers usually lack.
		opts = append(opts, chromedp.NoSandbox)
	}
	return opts
}

// ensureLocked lazily launches Chromium (m.mu must be held). If a previous
// instance died, it is torn down and relaunched once.
func (m *Manager) ensureLocked() error {
	if m.tabCtx != nil && m.healthyLocked() {
		return nil
	}
	m.teardownLocked()

	profileDir, err := os.MkdirTemp(m.cfg.WorkDir, ".forge-browser-")
	if err != nil {
		return fmt.Errorf("browser: create profile dir: %w", err)
	}
	m.profileDir = profileDir

	// The allocator parents context.Background(), not a tool-call context:
	// the browser must outlive individual tool calls.
	m.allocCtx, m.allocCancel = chromedp.NewExecAllocator(context.Background(), m.allocatorOptions()...)
	m.tabCtx, m.tabCancel = chromedp.NewContext(m.allocCtx)

	// Force the browser process to start now so launch errors surface here,
	// not on the first real action. The first Run MUST receive the undecorated
	// tab context: chromedp ties the browser lifetime to the context of the
	// Run that launches it, so a timeout-derived context would kill the
	// browser as soon as its cancel fires.
	if err := chromedp.Run(m.tabCtx); err != nil {
		m.teardownLocked()
		return fmt.Errorf("browser: launch %s: %w", m.cfg.BinaryPath, err)
	}
	return nil
}

// healthyLocked pings the tab with a trivial evaluation (m.mu must be held).
func (m *Manager) healthyLocked() bool {
	if m.tabCtx.Err() != nil {
		return false
	}
	pingCtx, cancel := context.WithTimeout(m.tabCtx, 2*time.Second)
	defer cancel()
	var one int
	return chromedp.Run(pingCtx, chromedp.Evaluate("1", &one)) == nil
}

func (m *Manager) teardownLocked() {
	if m.tabCancel != nil {
		m.tabCancel()
		m.tabCancel = nil
		m.tabCtx = nil
	}
	if m.allocCancel != nil {
		m.allocCancel()
		m.allocCancel = nil
		m.allocCtx = nil
	}
	if m.profileDir != "" {
		os.RemoveAll(m.profileDir) //nolint:errcheck
		m.profileDir = ""
	}
}

// run executes chromedp actions against the (lazily launched) browser under a
// per-call timeout. All tool calls funnel through here, serialized by m.mu.
func (m *Manager) run(timeout time.Duration, actions ...chromedp.Action) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.ensureLocked(); err != nil {
		return err
	}
	runCtx, cancel := context.WithTimeout(m.tabCtx, timeout)
	defer cancel()
	return chromedp.Run(runCtx, actions...)
}

// Stop shuts the browser down and removes the throwaway profile. Idempotent.
func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.teardownLocked()
}
