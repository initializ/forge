package browser

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
)

//go:embed snapshot.js
var snapshotJS string

//go:embed extract.js
var extractJS string

// ErrStale is returned when an interaction references a generation older than
// the current page state (the page navigated or was re-snapshotted since the
// LLM last saw it). Tools catch it and return a fresh digest so the model
// recovers in one turn.
var ErrStale = errors.New("stale element index: the page changed since the last snapshot")

// elementInfo mirrors one entry of snapshot.js's els array.
type elementInfo struct {
	Index     int      `json:"i"`
	Tag       string   `json:"tag"`
	Role      string   `json:"role"`
	Name      string   `json:"name"`
	Href      string   `json:"href,omitempty"`
	InputType string   `json:"inputType,omitempty"`
	Checked   *bool    `json:"checked,omitempty"`
	Options   []string `json:"options,omitempty"`
	Value     string   `json:"value,omitempty"`
	Protected bool     `json:"protected"`
	CX        float64  `json:"cx"`
	CY        float64  `json:"cy"`
}

// pageSnapshot mirrors snapshot.js's return value.
type pageSnapshot struct {
	URL      string        `json:"url"`
	Title    string        `json:"title"`
	Gen      int64         `json:"gen"`
	Els      []elementInfo `json:"els"`
	TotalEls int           `json:"totalEls"`
	Text     string        `json:"text"`
	TextLen  int           `json:"textLen"`
}

const (
	defaultMaxEls  = 100
	defaultMaxText = 1200
	settleDelay    = 300 * time.Millisecond
)

type point struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// snapshotAction captures a fresh snapshot into snap under a new generation.
// Must run inside m.run (m.mu held).
func (m *Manager) snapshotAction(snap *pageSnapshot, maxEls int) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		m.gen++
		opts, err := json.Marshal(map[string]any{
			"maxEls":  maxEls,
			"maxText": defaultMaxText,
			"gen":     m.gen,
		})
		if err != nil {
			return err
		}
		expr := "(" + snapshotJS + ")(" + string(opts) + ")"
		if err := chromedp.Evaluate(expr, snap).Do(ctx); err != nil {
			return fmt.Errorf("snapshot: %w", err)
		}
		return nil
	})
}

// checkGenAction verifies the page still matches the generation the LLM acted
// on. A missing __forge_gen (page navigated, window state wiped) or a mismatch
// yields ErrStale.
func checkGenAction(gen int64) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		var cur int64
		expr := "typeof window.__forge_gen === 'number' ? window.__forge_gen : -1"
		if err := chromedp.Evaluate(expr, &cur).Do(ctx); err != nil {
			return fmt.Errorf("generation check: %w", err)
		}
		if cur != gen {
			return ErrStale
		}
		return nil
	})
}

// settleAction waits briefly for click/fill side effects (navigation, SPA
// re-render) to land: fixed delay, then poll readyState out of 'loading'.
func settleAction() chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		if err := chromedp.Sleep(settleDelay).Do(ctx); err != nil {
			return err
		}
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			var state string
			if err := chromedp.Evaluate("document.readyState", &state).Do(ctx); err == nil && state != "loading" {
				return nil
			}
			if err := chromedp.Sleep(100 * time.Millisecond).Do(ctx); err != nil {
				return err
			}
		}
		return nil // settled or not, let the snapshot show reality
	})
}

// Navigate loads url and returns a fresh snapshot.
func (m *Manager) Navigate(url string, waitMS int, maxEls int) (pageSnapshot, error) {
	if maxEls <= 0 {
		maxEls = defaultMaxEls
	}
	extraWait := time.Duration(waitMS) * time.Millisecond
	if extraWait < 0 {
		extraWait = 0
	}
	if extraWait > 15*time.Second {
		extraWait = 15 * time.Second
	}
	var snap pageSnapshot
	err := m.run(m.cfg.NavTimeout,
		chromedp.Navigate(url),
		chromedp.Sleep(extraWait),
		settleAction(),
		m.snapshotAction(&snap, maxEls),
	)
	return snap, err
}

// Snapshot re-reads the current page (optionally scrolling first).
func (m *Manager) Snapshot(maxEls int, scrollToIndex int, scrollPages float64) (pageSnapshot, error) {
	if maxEls <= 0 {
		maxEls = defaultMaxEls
	}
	var actions []chromedp.Action
	if scrollToIndex >= 0 {
		actions = append(actions, chromedp.ActionFunc(func(ctx context.Context) error {
			var pt *point
			expr := fmt.Sprintf("window.__forge_center ? window.__forge_center(%d) : null", scrollToIndex)
			return chromedp.Evaluate(expr, &pt).Do(ctx) // scrollIntoView side effect; nil pt is fine
		}))
	} else if scrollPages != 0 {
		actions = append(actions, chromedp.ActionFunc(func(ctx context.Context) error {
			expr := fmt.Sprintf("window.scrollBy(0, window.innerHeight * %g); true", scrollPages)
			var ok bool
			return chromedp.Evaluate(expr, &ok).Do(ctx)
		}))
	}
	var snap pageSnapshot
	actions = append(actions, settleAction(), m.snapshotAction(&snap, maxEls))
	err := m.run(m.cfg.ActionTimeout, actions...)
	return snap, err
}

// Click resolves index to fresh viewport coordinates and dispatches a trusted
// CDP mouse click, then returns the post-action snapshot.
func (m *Manager) Click(index int, gen int64, maxEls int) (pageSnapshot, error) {
	if maxEls <= 0 {
		maxEls = defaultMaxEls
	}
	var snap pageSnapshot
	err := m.run(m.cfg.ActionTimeout,
		checkGenAction(gen),
		chromedp.ActionFunc(func(ctx context.Context) error {
			var pt *point
			expr := fmt.Sprintf("window.__forge_center(%d)", index)
			if err := chromedp.Evaluate(expr, &pt).Do(ctx); err != nil {
				return fmt.Errorf("resolve element %d: %w", index, err)
			}
			if pt == nil {
				return fmt.Errorf("%w (element [%d] no longer attached)", ErrStale, index)
			}
			return chromedp.MouseClickXY(pt.X, pt.Y).Do(ctx)
		}),
		settleAction(),
		m.snapshotAction(&snap, maxEls),
	)
	return snap, err
}

// Fill types text into the element at index: focus via trusted click, select
// existing content, replace it with CDP Input.insertText (fires native input
// events, so React/Vue controlled inputs see the change), then dispatch
// change. Select elements pick the matching option instead. Protected fields
// (password/payment autocomplete) are refused unless allowSensitive.
func (m *Manager) Fill(index int, text string, gen int64, submit bool, allowSensitive bool, maxEls int) (pageSnapshot, error) {
	if maxEls <= 0 {
		maxEls = defaultMaxEls
	}
	var snap pageSnapshot
	err := m.run(m.cfg.ActionTimeout,
		checkGenAction(gen),
		chromedp.ActionFunc(func(ctx context.Context) error {
			// Re-check protection in the live DOM, not a cached snapshot: a
			// hostile page could morph a text field into a password field
			// after the snapshot was taken.
			if !allowSensitive {
				var prot bool
				expr := fmt.Sprintf("window.__forge_protected(%d)", index)
				if err := chromedp.Evaluate(expr, &prot).Do(ctx); err != nil {
					return fmt.Errorf("protection check: %w", err)
				}
				if prot {
					return fmt.Errorf("field [%d] is fill-protected (password or payment input); the skill must opt in via guardrails browser.allow_sensitive_fill", index)
				}
			}

			var isSelect bool
			if err := chromedp.Evaluate(fmt.Sprintf(
				"(function(){var el=window.__forge_els[%d]; return !!el && el.tagName.toLowerCase()==='select';})()", index),
				&isSelect).Do(ctx); err != nil {
				return fmt.Errorf("resolve element %d: %w", index, err)
			}
			if isSelect {
				var ok bool
				expr := fmt.Sprintf("window.__forge_select_option(%d, %s)", index, mustJSON(text))
				if err := chromedp.Evaluate(expr, &ok).Do(ctx); err != nil {
					return fmt.Errorf("select option: %w", err)
				}
				if !ok {
					return fmt.Errorf("no option matching %q in select [%d]; see its options in the digest", text, index)
				}
				return nil
			}

			// Focus with a real click so focus/blur handlers fire.
			var pt *point
			if err := chromedp.Evaluate(fmt.Sprintf("window.__forge_center(%d)", index), &pt).Do(ctx); err != nil {
				return fmt.Errorf("resolve element %d: %w", index, err)
			}
			if pt == nil {
				return fmt.Errorf("%w (element [%d] no longer attached)", ErrStale, index)
			}
			if err := chromedp.MouseClickXY(pt.X, pt.Y).Do(ctx); err != nil {
				return err
			}
			var selected bool
			if err := chromedp.Evaluate(fmt.Sprintf("window.__forge_select_all(%d)", index), &selected).Do(ctx); err != nil {
				return fmt.Errorf("select existing content: %w", err)
			}
			if err := input.InsertText(text).Do(ctx); err != nil {
				return fmt.Errorf("insert text: %w", err)
			}
			var changed bool
			if err := chromedp.Evaluate(fmt.Sprintf("window.__forge_dispatch_change(%d)", index), &changed).Do(ctx); err != nil {
				return fmt.Errorf("dispatch change: %w", err)
			}
			if submit {
				return chromedp.KeyEvent(kb.Enter).Do(ctx)
			}
			return nil
		}),
		settleAction(),
		m.snapshotAction(&snap, maxEls),
	)
	return snap, err
}

// Extract returns the page content in the requested mode ("text", "links",
// "html") plus the current URL. Pagination happens in the tool layer.
func (m *Manager) Extract(mode, selector string) (content string, url string, err error) {
	err = m.run(m.cfg.ActionTimeout,
		chromedp.Location(&url),
		chromedp.ActionFunc(func(ctx context.Context) error {
			expr := "(" + extractJS + ")(" + mustJSON(mode) + ", " + mustJSON(selector) + ")"
			return chromedp.Evaluate(expr, &content).Do(ctx)
		}),
	)
	return content, url, err
}

// Screenshot captures the viewport (or full page) as PNG bytes.
func (m *Manager) Screenshot(fullPage bool) ([]byte, error) {
	var buf []byte
	var action chromedp.Action
	if fullPage {
		action = chromedp.FullScreenshot(&buf, 100)
	} else {
		action = chromedp.CaptureScreenshot(&buf)
	}
	if err := m.run(m.cfg.ActionTimeout, action); err != nil {
		return nil, err
	}
	return buf, nil
}

// mustJSON marshals a string for safe embedding in a JS expression.
func mustJSON(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		// strings always marshal; keep the assertion visible
		panic(fmt.Sprintf("browser: marshal string: %v", err))
	}
	return string(b)
}

// truncate returns s capped at max runes-ish (byte-safe on ASCII, close
// enough for caps) with a marker.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8Start(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }
