package forgeui

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seedAgentSkills writes a forge.yaml plus a fixed set of skills under
// skills/ so the list + load handlers have something to discover. The
// shape mirrors what `forge init` + `forge skills add` produce in the
// wild — subdir form with helper scripts and a flat single-file form.
func seedAgentSkills(t *testing.T) (root string, agentID string) {
	t.Helper()
	isolateHome(t)
	root = t.TempDir()
	agentID = "edit-test-agent"
	agentDir := filepath.Join(root, agentID)
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(agentDir, "forge.yaml"), `agent_id: edit-test-agent
version: 0.1.0
framework: forge
model:
  provider: openai
  name: gpt-4o
`)

	// Subdir skill with a helper script.
	subdir := filepath.Join(agentDir, "skills", "foo")
	if err := os.MkdirAll(filepath.Join(subdir, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(subdir, "SKILL.md"), `---
name: foo
description: A foo skill
category: ops
tags: [foo, search]
---

# Foo

## Tool: foo_search

Search for foo.

**Input:** query (string)
**Output:** JSON results
`)
	writeFile(t, filepath.Join(subdir, "scripts", "foo_search.sh"), "#!/usr/bin/env bash\necho foo\n")

	// Flat single-file skill (no scripts dir).
	writeFile(t, filepath.Join(agentDir, "skills", "bar.md"), `---
name: bar
description: A flat-form skill
---

# Bar

## Tool: bar_do

Do bar.
`)

	// Subdir skill without scripts/ — exercises HasScripts=false.
	baz := filepath.Join(agentDir, "skills", "baz")
	if err := os.MkdirAll(baz, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(baz, "SKILL.md"), `---
name: baz
description: Subdir but no scripts
---

# Baz

## Tool: baz_run

Run baz.
`)

	return root, agentID
}

func newSkillEditServer(t *testing.T, root string) *UIServer {
	t.Helper()
	return NewUIServer(UIServerConfig{
		Port:      4200,
		WorkDir:   root,
		ExePath:   "/usr/bin/false",
		AgentPort: 9100,
	})
}

func TestListCustomSkills_ReturnsAllForms(t *testing.T) {
	root, agentID := seedAgentSkills(t)
	srv := newSkillEditServer(t, root)

	req := httptest.NewRequest(http.MethodGet,
		"/api/agents/"+agentID+"/skill-builder/skills", nil)
	req.SetPathValue("id", agentID)
	w := httptest.NewRecorder()
	srv.handleSkillBuilderListCustomSkills(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	var got []CustomSkillSummary
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 skills, got %d: %+v", len(got), got)
	}
	byName := map[string]CustomSkillSummary{}
	for _, s := range got {
		byName[s.Name] = s
	}

	foo, ok := byName["foo"]
	if !ok {
		t.Fatalf("foo missing; got %+v", got)
	}
	if !foo.HasScripts {
		t.Errorf("foo.HasScripts = false, want true (skills/foo/scripts/ exists)")
	}
	if foo.Description != "A foo skill" {
		t.Errorf("foo.Description = %q", foo.Description)
	}
	if len(foo.Tools) != 1 || foo.Tools[0] != "foo_search" {
		t.Errorf("foo.Tools = %+v, want [foo_search]", foo.Tools)
	}
	if foo.Path != filepath.Join("skills", "foo", "SKILL.md") {
		t.Errorf("foo.Path = %q", foo.Path)
	}

	bar, ok := byName["bar"]
	if !ok {
		t.Fatalf("bar missing")
	}
	if bar.HasScripts {
		t.Errorf("bar.HasScripts = true, want false (flat form has no scripts dir)")
	}
	if bar.Path != filepath.Join("skills", "bar.md") {
		t.Errorf("bar.Path = %q", bar.Path)
	}

	baz, ok := byName["baz"]
	if !ok {
		t.Fatalf("baz missing")
	}
	if baz.HasScripts {
		t.Errorf("baz.HasScripts = true, want false (no scripts/ subdir)")
	}
}

func TestListCustomSkills_EmptyAgent(t *testing.T) {
	isolateHome(t)
	root := t.TempDir()
	agentDir := filepath.Join(root, "empty-agent")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(agentDir, "forge.yaml"), `agent_id: empty-agent
version: 0.1.0
framework: forge
model:
  provider: openai
  name: gpt-4o
`)

	srv := newSkillEditServer(t, root)
	req := httptest.NewRequest(http.MethodGet,
		"/api/agents/empty-agent/skill-builder/skills", nil)
	req.SetPathValue("id", "empty-agent")
	w := httptest.NewRecorder()
	srv.handleSkillBuilderListCustomSkills(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	var got []CustomSkillSummary
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty list, got %+v", got)
	}
}

func TestGetCustomSkill_Subdir(t *testing.T) {
	root, agentID := seedAgentSkills(t)
	srv := newSkillEditServer(t, root)

	req := httptest.NewRequest(http.MethodGet,
		"/api/agents/"+agentID+"/skill-builder/skills/foo", nil)
	req.SetPathValue("id", agentID)
	req.SetPathValue("name", "foo")
	w := httptest.NewRecorder()
	srv.handleSkillBuilderGetCustomSkill(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	var got CustomSkillContent
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Format != "subdir" {
		t.Errorf("Format = %q, want subdir", got.Format)
	}
	if !strings.Contains(got.SkillMD, "name: foo") {
		t.Errorf("SkillMD missing frontmatter; got: %q", got.SkillMD)
	}
	if _, ok := got.Scripts["foo_search.sh"]; !ok {
		t.Errorf("expected foo_search.sh in scripts; got %+v", got.Scripts)
	}
}

func TestGetCustomSkill_Flat(t *testing.T) {
	root, agentID := seedAgentSkills(t)
	srv := newSkillEditServer(t, root)

	req := httptest.NewRequest(http.MethodGet,
		"/api/agents/"+agentID+"/skill-builder/skills/bar", nil)
	req.SetPathValue("id", agentID)
	req.SetPathValue("name", "bar")
	w := httptest.NewRecorder()
	srv.handleSkillBuilderGetCustomSkill(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	var got CustomSkillContent
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Format != "flat" {
		t.Errorf("Format = %q, want flat", got.Format)
	}
	if len(got.Scripts) != 0 {
		t.Errorf("flat form must not return scripts; got %+v", got.Scripts)
	}
}

// TestGetCustomSkill_Errors pins the security-sensitive error paths
// for the load endpoint. The skill name is a path component, so each
// of these inputs MUST fail before the handler touches the filesystem.
func TestGetCustomSkill_Errors(t *testing.T) {
	root, agentID := seedAgentSkills(t)
	srv := newSkillEditServer(t, root)

	cases := []struct {
		name string
		raw  string
		want int
	}{
		{"missing", "no-such-skill", http.StatusNotFound},
		{"path-traversal", "..", http.StatusBadRequest},
		{"slash", "foo/bar", http.StatusBadRequest},
		{"backslash", `foo\bar`, http.StatusBadRequest},
		{"non-kebab", "Foo_Bar", http.StatusBadRequest},
		{"empty", "", http.StatusBadRequest},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet,
				"/api/agents/"+agentID+"/skill-builder/skills/"+c.raw, nil)
			req.SetPathValue("id", agentID)
			req.SetPathValue("name", c.raw)
			w := httptest.NewRecorder()
			srv.handleSkillBuilderGetCustomSkill(w, req)
			if w.Code != c.want {
				t.Errorf("name=%q: status = %d, want %d; body: %s",
					c.raw, w.Code, c.want, w.Body.String())
			}
		})
	}
}

// TestReadCustomSkill_SymlinkEscapeRejected pins the script-loader's
// symlink guard. A skill directory is trusted, but a symlink INSIDE
// it that resolves to a file outside the directory (e.g. /etc/passwd
// or a sibling skill's secret) must not surface in the editor — and
// from there into the LLM context. The link is silently dropped from
// the scripts map; the rest of the skill still loads.
func TestReadCustomSkill_SymlinkEscapeRejected(t *testing.T) {
	root, agentID := seedAgentSkills(t)

	scriptsDir := filepath.Join(root, agentID, "skills", "foo", "scripts")
	// Create a file OUTSIDE the skill directory.
	outside := filepath.Join(root, "secret.txt")
	if err := os.WriteFile(outside, []byte("SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Symlink inside scripts/ pointing at it.
	link := filepath.Join(scriptsDir, "leak.sh")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unsupported on this platform: %v", err)
	}

	srv := newSkillEditServer(t, root)
	req := httptest.NewRequest(http.MethodGet,
		"/api/agents/"+agentID+"/skill-builder/skills/foo", nil)
	req.SetPathValue("id", agentID)
	req.SetPathValue("name", "foo")
	w := httptest.NewRecorder()
	srv.handleSkillBuilderGetCustomSkill(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	var got CustomSkillContent
	_ = json.NewDecoder(w.Body).Decode(&got)
	if _, leaked := got.Scripts["leak.sh"]; leaked {
		t.Errorf("symlink leak: outside-of-skill file surfaced as a script (content: %q)", got.Scripts["leak.sh"])
	}
	// The legitimate sibling script must still be present.
	if _, ok := got.Scripts["foo_search.sh"]; !ok {
		t.Errorf("legitimate script foo_search.sh dropped alongside the symlink reject")
	}
}

// TestChat_EditMode_PrimesPromptWithExistingSkill captures the
// system prompt the chat handler sends to the LLM. In edit mode the
// prompt MUST include the "## Edit Mode" trailer and the on-disk
// SKILL.md content — the single-source-of-truth invariant means the
// handler loads from disk itself rather than trusting the request
// body. Issue #193.
func TestChat_EditMode_PrimesPromptWithExistingSkill(t *testing.T) {
	root, agentID := seedAgentSkills(t)

	// Tier-1 workspace LLM config so LoadSkillBuilderLLM resolves to
	// a fully-credentialed config without depending on the dev
	// machine's HOME or env.
	wsConfig := filepath.Join(root, ".forge", "ui.yaml")
	if err := os.MkdirAll(filepath.Dir(wsConfig), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, wsConfig, `skill_builder:
  provider: openai
  model: gpt-4o
  api_key_env: OPENAI_API_KEY
`)
	t.Setenv("OPENAI_API_KEY", "test-key")

	var captured string
	srv := NewUIServer(UIServerConfig{
		Port:      4200,
		WorkDir:   root,
		ExePath:   "/usr/bin/false",
		AgentPort: 9100,
		LLMStreamFunc: func(_ context.Context, opts LLMStreamOptions) error {
			captured = opts.SystemPrompt
			opts.OnDone("ack")
			return nil
		},
	})

	body, _ := json.Marshal(SkillBuilderChatRequest{
		Mode:        "edit",
		EditingName: "foo",
		Messages: []SkillBuilderMessage{
			{Role: "user", Content: "add a flag to skip the cache"},
		},
	})
	req := httptest.NewRequest(http.MethodPost,
		"/api/agents/"+agentID+"/skill-builder/chat", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", agentID)
	w := httptest.NewRecorder()
	srv.handleSkillBuilderChat(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	if !strings.Contains(captured, "## Edit Mode") {
		t.Errorf("system prompt missing '## Edit Mode' trailer; got:\n%s", captured)
	}
	if !strings.Contains(captured, "name: foo") {
		t.Errorf("system prompt missing the on-disk SKILL.md frontmatter; got:\n%s", captured)
	}
	if !strings.Contains(captured, "current-script:foo_search.sh") {
		t.Errorf("system prompt missing the on-disk helper script reference; got:\n%s", captured)
	}
}

// TestChat_CreateMode_OmitsEditTrailer pins the inverse: create mode
// must NEVER ship the edit-mode trailer or any reference to an
// existing skill. Otherwise an attacker could trick the LLM into
// scraping a skill's source by sending mode=create with garbage in
// the request — the handler ignores mode≠"edit" entirely.
func TestChat_CreateMode_OmitsEditTrailer(t *testing.T) {
	root, agentID := seedAgentSkills(t)

	wsConfig := filepath.Join(root, ".forge", "ui.yaml")
	if err := os.MkdirAll(filepath.Dir(wsConfig), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, wsConfig, `skill_builder:
  provider: openai
  model: gpt-4o
  api_key_env: OPENAI_API_KEY
`)
	t.Setenv("OPENAI_API_KEY", "test-key")

	var captured string
	srv := NewUIServer(UIServerConfig{
		Port:      4200,
		WorkDir:   root,
		ExePath:   "/usr/bin/false",
		AgentPort: 9100,
		LLMStreamFunc: func(_ context.Context, opts LLMStreamOptions) error {
			captured = opts.SystemPrompt
			opts.OnDone("ack")
			return nil
		},
	})

	body, _ := json.Marshal(SkillBuilderChatRequest{
		Messages: []SkillBuilderMessage{
			{Role: "user", Content: "make me a fresh skill"},
		},
	})
	req := httptest.NewRequest(http.MethodPost,
		"/api/agents/"+agentID+"/skill-builder/chat", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", agentID)
	w := httptest.NewRecorder()
	srv.handleSkillBuilderChat(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	if strings.Contains(captured, "## Edit Mode") {
		t.Errorf("create-mode prompt leaked the edit trailer:\n%s", captured)
	}
	if strings.Contains(captured, "current-skill.md") {
		t.Errorf("create-mode prompt leaked existing skill content marker")
	}
}

// TestSave_OverwriteMismatchedEditingName_Rejected pins the defense
// in depth: overwrite=true with an editing_name that doesn't match
// skill_name is rejected at the handler before SkillSaveFunc runs,
// so a malicious request can never use the edit path to clobber a
// DIFFERENT skill's directory.
func TestSave_OverwriteMismatchedEditingName_Rejected(t *testing.T) {
	srv, _ := setupTestServerWithSkillBuilder(t)
	body, _ := json.Marshal(SkillBuilderSaveRequest{
		SkillName:   "victim",
		EditingName: "attacker",
		Overwrite:   true,
		SkillMD:     "---\nname: victim\ndescription: x\n---\n# v\n## Tool: t\nA tool.\n",
	})
	req := httptest.NewRequest(http.MethodPost,
		"/api/agents/test-agent/skill-builder/save", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", "test-agent")
	w := httptest.NewRecorder()
	srv.handleSkillBuilderSave(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}
