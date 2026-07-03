package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/security"
	coretools "github.com/initializ/forge/forge-core/tools"
)

const indexHTML = `<!DOCTYPE html>
<html><head><title>Forge Test Index</title></head>
<body>
<nav><a href="/two" id="go2">Go to page two</a></nav>
<button onclick="document.getElementById('echo').textContent='button-clicked'">Do thing</button>
<form action="/submitted" method="get">
  <label>Work email <input type="email" name="email" placeholder="Work email"></label>
  <label>Password <input type="password" name="pw" placeholder="Password"></label>
  <label>Plan
    <select name="plan">
      <option>Starter</option>
      <option>Pro</option>
      <option>Enterprise</option>
    </select>
  </label>
  <button type="submit">Sign up</button>
</form>
<div id="echo"></div>
<p>Welcome to the forge browser test fixture. Pro plan $49/user/month.</p>
<script>
  document.querySelector('input[name=email]').addEventListener('input', function(e){
    document.getElementById('echo').textContent = 'typed:' + e.target.value;
  });
</script>
</body></html>`

const twoHTML = `<!DOCTYPE html>
<html><head><title>Page Two</title></head>
<body><h1>Second page</h1><a href="/">Back home</a></body></html>`

func articleHTML() string {
	var b strings.Builder
	b.WriteString(`<!DOCTYPE html><html><head><title>Long Article</title></head><body>`)
	b.WriteString(`<nav><a href="/">Back home</a></nav>`)
	b.WriteString(`<h1>The Article</h1>`)
	b.WriteString(`<table id="tbl"><tr><th>Plan</th><th>Price</th></tr><tr><td>Pro</td><td>$49</td></tr></table>`)
	for i := 0; i < 400; i++ {
		fmt.Fprintf(&b, `<p>Paragraph %d: the quick brown fox jumps over the lazy dog again and again, padding out this article for pagination tests.</p>`, i)
	}
	b.WriteString(`</body></html>`)
	return b.String()
}

func testServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	page := func(html string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte(html)) //nolint:errcheck
		}
	}
	mux.HandleFunc("/", page(indexHTML))
	mux.HandleFunc("/two", page(twoHTML))
	mux.HandleFunc("/article", page(articleHTML()))
	mux.HandleFunc("/submitted", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<html><head><title>Submitted</title></head><body><p>email=%s plan=%s</p></body></html>`,
			r.URL.Query().Get("email"), r.URL.Query().Get("plan"))
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

// findEl locates an element by (partial) accessible name.
func findEl(t *testing.T, snap pageSnapshot, name string) elementInfo {
	t.Helper()
	for _, el := range snap.Els {
		if strings.Contains(el.Name, name) {
			return el
		}
	}
	t.Fatalf("no element with name containing %q in snapshot (els: %+v)", name, snap.Els)
	return elementInfo{}
}

// TestE2EBrowserFlow drives the full digest → act-by-index → digest loop
// against a real Chromium routed through a real EgressProxy.
func TestE2EBrowserFlow(t *testing.T) {
	bin := requireChromium(t)
	ts := testServer(t)
	matcher := security.NewDomainMatcher(security.ModeAllowlist, nil)
	proxyURL, _, _ := startProxy(t, matcher)

	m, err := NewManager(Config{BinaryPath: bin, Headless: true, ProxyURL: proxyURL, WorkDir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer m.Stop()

	var home pageSnapshot

	t.Run("navigate digest", func(t *testing.T) {
		home, err = m.Navigate(ts.URL+"/", 0, 0)
		if err != nil {
			t.Fatalf("navigate: %v", err)
		}
		if home.Title != "Forge Test Index" {
			t.Errorf("title = %q", home.Title)
		}
		pw := findEl(t, home, "Password")
		if !pw.Protected {
			t.Error("password input not marked protected")
		}
		plan := findEl(t, home, "Plan")
		if len(plan.Options) < 3 || plan.Options[1] != "Pro" {
			t.Errorf("select options = %v", plan.Options)
		}
		d := buildDigest(home)
		if !strings.Contains(d, "⚠ fill-protected") {
			t.Errorf("digest missing protection mark:\n%s", d)
		}
	})

	t.Run("click link navigates", func(t *testing.T) {
		link := findEl(t, home, "Go to page two")
		snap, err := m.Click(link.Index, home.Gen, 0)
		if err != nil {
			t.Fatalf("click: %v", err)
		}
		if snap.Title != "Page Two" {
			t.Errorf("after click: title = %q, url = %q", snap.Title, snap.URL)
		}
	})

	t.Run("stale generation detected", func(t *testing.T) {
		// home.Gen is from before the click navigation — must be stale now.
		_, err := m.Click(0, home.Gen, 0)
		if !errors.Is(err, ErrStale) {
			t.Fatalf("click with old generation: err = %v, want ErrStale", err)
		}
		// Tool layer turns it into a recovery digest, not a bare error.
		tool := &clickTool{m: m}
		out, err := tool.Execute(context.Background(),
			json.RawMessage(fmt.Sprintf(`{"index":0,"generation":%d}`, home.Gen)))
		if err != nil {
			t.Fatalf("tool stale click returned error %v, want recovery digest", err)
		}
		if !strings.Contains(out, "stale element index") || !strings.Contains(out, "Generation:") {
			t.Errorf("stale recovery digest malformed:\n%s", out)
		}
	})

	t.Run("fill input fires events", func(t *testing.T) {
		home, err = m.Navigate(ts.URL+"/", 0, 0)
		if err != nil {
			t.Fatalf("navigate: %v", err)
		}
		email := findEl(t, home, "Work email")
		snap, err := m.Fill(email.Index, "mk@example.com", home.Gen, false, false, 0)
		if err != nil {
			t.Fatalf("fill: %v", err)
		}
		// The page's input listener echoes the value into #echo; it shows up
		// in the snapshot text only if native input events fired.
		if !strings.Contains(snap.Text, "typed:mk@example.com") {
			t.Errorf("input event did not fire; page text: %q", snap.Text)
		}
		home = snap
	})

	t.Run("protected fill refused then allowed", func(t *testing.T) {
		pw := findEl(t, home, "Password")
		_, err := m.Fill(pw.Index, "hunter2", home.Gen, false, false, 0)
		if err == nil || !strings.Contains(err.Error(), "fill-protected") {
			t.Fatalf("protected fill: err = %v, want fill-protected refusal", err)
		}
		snap, err := m.Fill(pw.Index, "hunter2", home.Gen, false, true, 0)
		if err != nil {
			t.Fatalf("opted-in protected fill: %v", err)
		}
		home = snap
	})

	t.Run("fill select picks option", func(t *testing.T) {
		plan := findEl(t, home, "Plan")
		snap, err := m.Fill(plan.Index, "Pro", home.Gen, false, false, 0)
		if err != nil {
			t.Fatalf("fill select: %v", err)
		}
		if got := findEl(t, snap, "Plan").Value; got != "Pro" {
			t.Errorf("select value = %q, want Pro", got)
		}
		_, err = m.Fill(plan.Index, "Nonexistent Tier", snap.Gen, false, false, 0)
		if err == nil || !strings.Contains(err.Error(), "no option matching") {
			t.Errorf("bogus option: err = %v", err)
		}
	})

	t.Run("fill with submit navigates form", func(t *testing.T) {
		home, err = m.Navigate(ts.URL+"/", 0, 0)
		if err != nil {
			t.Fatalf("navigate: %v", err)
		}
		email := findEl(t, home, "Work email")
		snap, err := m.Fill(email.Index, "submit@example.com", home.Gen, true, false, 0)
		if err != nil {
			t.Fatalf("fill+submit: %v", err)
		}
		if !strings.Contains(snap.URL, "/submitted") || !strings.Contains(snap.Text, "email=submit@example.com") {
			t.Errorf("form did not submit: url = %q text = %q", snap.URL, snap.Text)
		}
	})

	t.Run("extract pagination", func(t *testing.T) {
		if _, err := m.Navigate(ts.URL+"/article", 0, 0); err != nil {
			t.Fatalf("navigate: %v", err)
		}
		tool := &extractTool{m: m}

		out, err := tool.Execute(context.Background(), json.RawMessage(`{"mode":"text","max_chars":1000}`))
		if err != nil {
			t.Fatalf("extract: %v", err)
		}
		if !strings.Contains(out, "chars 0-") || !strings.Contains(out, "(pass offset=") {
			t.Errorf("pagination header missing:\n%.200s", out)
		}
		if !strings.Contains(out, "# The Article") {
			t.Errorf("markdown heading missing:\n%.300s", out)
		}

		out2, err := tool.Execute(context.Background(), json.RawMessage(`{"mode":"text","max_chars":1000,"offset":1000}`))
		if err != nil {
			t.Fatalf("extract offset: %v", err)
		}
		if !strings.Contains(out2, "chars 1000-") && !strings.Contains(out2, "chars 99") {
			t.Errorf("offset header wrong:\n%.200s", out2)
		}

		links, err := tool.Execute(context.Background(), json.RawMessage(`{"mode":"links"}`))
		if err != nil {
			t.Fatalf("extract links: %v", err)
		}
		if !strings.Contains(links, "[Back home](") {
			t.Errorf("links mode missing anchor:\n%s", links)
		}

		html, err := tool.Execute(context.Background(), json.RawMessage(`{"mode":"html","selector":"#tbl"}`))
		if err != nil {
			t.Fatalf("extract html: %v", err)
		}
		if !strings.Contains(html, "<table") {
			t.Errorf("html mode missing table:\n%.300s", html)
		}

		if _, err := tool.Execute(context.Background(), json.RawMessage(`{"mode":"html"}`)); err == nil {
			t.Error("html mode without selector succeeded, want refusal")
		}
	})

	t.Run("screenshot artifact shape", func(t *testing.T) {
		tool := &screenshotTool{m: m}
		out, err := tool.Execute(context.Background(), json.RawMessage(`{"filename":"e2e shot.png"}`))
		if err != nil {
			t.Fatalf("screenshot: %v", err)
		}
		var res map[string]any
		if err := json.Unmarshal([]byte(out), &res); err != nil {
			t.Fatalf("screenshot result not JSON: %v\n%s", err, out)
		}
		if _, hasContent := res["content"]; hasContent {
			t.Error("screenshot result contains inline content; must be path-only")
		}
		path, _ := res["path"].(string)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read screenshot: %v", err)
		}
		if len(data) < 8 || string(data[1:4]) != "PNG" {
			t.Errorf("screenshot is not a PNG (%d bytes)", len(data))
		}
	})

	t.Run("blocked domain reads as policy", func(t *testing.T) {
		tool := &navigateTool{m: m}
		_, err := tool.Execute(context.Background(), json.RawMessage(`{"url":"https://blocked.example.test/"}`))
		if err == nil || !strings.Contains(err.Error(), "egress policy") {
			t.Errorf("blocked navigate: err = %v, want egress policy message", err)
		}
	})
}

// TestNavigateToolRejectsSchemes needs no browser: validation happens before
// the manager is touched.
func TestNavigateToolRejectsSchemes(t *testing.T) {
	tool := &navigateTool{m: nil}
	for _, u := range []string{
		"file:///etc/passwd",
		"javascript:alert(1)",
		"data:text/html,hi",
		"chrome://settings",
		"example.com/no-scheme",
	} {
		if _, err := tool.Execute(context.Background(), json.RawMessage(`{"url":"`+u+`"}`)); err == nil {
			t.Errorf("navigate %q succeeded, want scheme rejection", u)
		}
	}
}

// TestRegisterTools verifies registration and the fail-closed proxy check.
func TestRegisterTools(t *testing.T) {
	reg := coretools.NewRegistry()
	m := &Manager{cfg: Config{ProxyURL: "http://127.0.0.1:1", BinaryPath: "/x", WorkDir: t.TempDir()}}
	if err := RegisterTools(reg, m); err != nil {
		t.Fatalf("RegisterTools: %v", err)
	}
	for _, name := range ToolNames {
		if reg.Get(name) == nil {
			t.Errorf("tool %s not registered", name)
		}
	}
	if err := RegisterTools(coretools.NewRegistry(), &Manager{cfg: Config{}}); err == nil {
		t.Error("RegisterTools without proxy succeeded, want refusal")
	}
}
