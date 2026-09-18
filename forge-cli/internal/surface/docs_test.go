package surface

import (
	"strings"
	"testing"
)

func TestSectionsParsed(t *testing.T) {
	// forge.md has ## 1..20; a representative few must be indexed by number.
	for _, n := range []int{1, 5, 12, 14, 16, 20} {
		if _, ok := sectionByNum[n]; !ok {
			t.Errorf("section %d not parsed from forge.md", n)
		}
	}
	if len(knowledgeChunks) < 30 {
		t.Errorf("expected many search chunks, got %d", len(knowledgeChunks))
	}
}

func TestRenderTopic(t *testing.T) {
	for _, name := range TopicNames() {
		doc, ok := RenderTopic(name)
		if !ok {
			t.Errorf("topic %q not renderable", name)
			continue
		}
		if strings.TrimSpace(doc) == "" {
			t.Errorf("topic %q rendered empty", name)
		}
		if len(doc) > maxDocChars+200 {
			t.Errorf("topic %q exceeded cap: %d", name, len(doc))
		}
	}
	if _, ok := RenderTopic("nope"); ok {
		t.Error("unknown topic should not be ok")
	}
}

func TestCreateSkillIncludesBuilderGuide(t *testing.T) {
	if len(skillBuilderSections) == 0 {
		t.Fatal("skill-builder guide not parsed")
	}
	doc, _ := RenderTopic("create-skill")
	// A heading unique to the skill-builder guide should appear (best-effort:
	// at least one of its section titles).
	found := false
	for _, s := range skillBuilderSections {
		if s.title != "" && strings.Contains(doc, s.title) {
			found = true
			break
		}
	}
	if !found {
		t.Error("create-skill topic did not include the skill-builder guide")
	}
}

func TestSearchDocs(t *testing.T) {
	got := strings.ToLower(SearchDocs("how do I wire a slack channel"))
	if !strings.Contains(got, "channel") && !strings.Contains(got, "slack") {
		t.Errorf("search for slack channel returned unrelated content: %.120q", got)
	}
	if strings.TrimSpace(SearchDocs("")) == "" {
		t.Error("empty query should fall back to overview, not empty")
	}
}

func TestForgeDocsDispatch(t *testing.T) {
	// A valid topic returns that topic.
	if !strings.Contains(ForgeDocs("forge-yaml", ""), "forge.yaml") {
		t.Error("ForgeDocs(topic=forge-yaml) should mention forge.yaml")
	}
	// An unknown topic degrades to search rather than erroring/empty.
	if strings.TrimSpace(ForgeDocs("bogus", "")) == "" {
		t.Error("ForgeDocs(unknown topic) should degrade to search")
	}
}

func TestForgeDocsHonorsQueryWithTopic(t *testing.T) {
	// When both a topic AND a query are given, the query must still be honored.
	got := ForgeDocs("create-agent", "import an existing skill folder")
	if !strings.Contains(got, "Search results for:") {
		t.Errorf("expected search results section when topic+query given: %.120q", got)
	}
	low := strings.ToLower(got)
	// The direct import commands live in the CLI section; the search should reach them.
	if !strings.Contains(low, "import") {
		t.Errorf("topic+query result should mention import: %.160q", low)
	}
}

func TestSearchSurfacesSkillImport(t *testing.T) {
	low := strings.ToLower(SearchDocs("import external skill folder from-skill-dir"))
	if !strings.Contains(low, "import") && !strings.Contains(low, "from-skill-dir") {
		t.Errorf("search should surface the skill-import CLI path: %.160q", low)
	}
}
