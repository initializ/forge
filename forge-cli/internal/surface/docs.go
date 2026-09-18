package surface

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/initializ/forge/forge-core/tools"
)

// forgeKnowledgeMD is the canonical Forge knowledge doc (a copy of the repo's
// .claude/skills/forge.md), embedded so the surface — and the forge_docs tool
// it exposes to a coding agent — can answer how-to questions and ground agent
// building without a network call. Re-sync with `make sync-knowledge` if the
// source drifts.
//
//go:embed knowledge/forge.md
var forgeKnowledgeMD string

// skillBuilderMD is the dedicated skill-authoring guide (a copy of the repo's
// .claude/skills/forge-skill-builder.md). It deepens the create-skill topic and
// feeds the search index.
//
//go:embed knowledge/forge-skill-builder.md
var skillBuilderMD string

// maxDocChars caps a single forge_docs response so a topic dump (some sections
// run long) can't blow the caller's context. Query mode returns the top chunks
// under the same ceiling.
const maxDocChars = 9000

// docSection is one `## ` heading block of the knowledge doc.
type docSection struct {
	num   int    // leading section number ("## 12. …" → 12), 0 when absent
	title string // heading text after "## "
	body  string // content up to the next "## " heading (fence-aware)
}

// docChunk is a finer retrieval unit: a section optionally split at its `### `
// subheadings, so query mode returns a focused passage, not a 300-line section.
type docChunk struct {
	heading string // breadcrumb, e.g. "12. Security model › Egress"
	text    string
}

// Topic groups related sections under a stable name used by both the forge_docs
// tool and the native-path intent router.
type Topic struct {
	Name        string
	Description string
	sections    []int // section numbers whose bodies make up the topic
}

// topics is the curated topic → section(s) map over forge.md's `## 1..20`.
var topics = []Topic{
	{"overview", "What Forge is, and the module layout", []int{1, 2}},
	{"how-it-works", "How an agent runs end-to-end; the A2A protocol surface", []int{3, 4}},
	{"models", "LLM providers, models, and credentials", []int{5}},
	{"tools", "The tool system and built-in tools (incl. the MCP client)", []int{6}},
	{"channels", "Slack / Telegram / MS Teams channel adapters", []int{7}},
	{"mcp", "Wiring MCP servers (auth types, discovery) — see tools + forge.yaml", []int{6, 14}},
	{"memory", "The agent memory system", []int{8}},
	{"scheduling", "Cron schedules and delivery", []int{9}},
	{"secrets", "Encrypted secrets management", []int{10}},
	{"security", "Security model: egress, policy layers, DEFER approvals, audit", []int{12}},
	{"build-deploy", "Build pipeline, packaging, and deploy to Kubernetes", []int{11, 15}},
	{"cli", "The full forge CLI surface", []int{13}},
	{"forge-yaml", "forge.yaml schema reference", []int{14}},
	{"create-agent", "Step-by-step: create an agent", []int{15}},
	{"create-skill", "Step-by-step: write a SKILL.md skill", []int{16}},
	{"audit", "Audit event reference", []int{17}},
	{"recipes", "Common questions and recipes", []int{20}},
}

// parsed knowledge, built once on first use.
var (
	knowledgeSections    []docSection
	skillBuilderSections []docSection
	knowledgeChunks      []docChunk
	sectionByNum         map[int]docSection
)

func init() {
	knowledgeSections = parseSections(forgeKnowledgeMD)
	sectionByNum = make(map[int]docSection, len(knowledgeSections))
	for _, s := range knowledgeSections {
		if s.num > 0 {
			sectionByNum[s.num] = s
		}
	}
	skillBuilderSections = parseSections(stripFrontmatter(skillBuilderMD))
	// Both docs feed the search index; the skill-builder guide also enriches the
	// create-skill topic (see RenderTopic).
	knowledgeChunks = buildChunks(knowledgeSections)
	knowledgeChunks = append(knowledgeChunks, buildChunks(skillBuilderSections)...)
}

// stripFrontmatter drops a leading `---`…`---` YAML block (skill docs carry one)
// so its keys don't pollute the section parse.
func stripFrontmatter(s string) string {
	s = strings.TrimPrefix(s, "\ufeff")
	if !strings.HasPrefix(s, "---\n") {
		return s
	}
	if end := strings.Index(s[4:], "\n---"); end >= 0 {
		rest := s[4+end+len("\n---"):]
		return strings.TrimPrefix(rest, "\n")
	}
	return s
}

// parseSections splits the doc into `## ` heading blocks. It tracks fenced code
// blocks so `#`/`##` lines inside ``` fences (e.g. the `# 1. Scaffold` shell
// comments in the create-agent section) are not mistaken for headings.
func parseSections(md string) []docSection {
	var out []docSection
	var cur *docSection
	var body strings.Builder
	inFence := false

	flush := func() {
		if cur != nil {
			cur.body = strings.TrimSpace(body.String())
			out = append(out, *cur)
		}
		body.Reset()
	}

	for _, line := range strings.Split(md, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
		}
		if !inFence && strings.HasPrefix(line, "## ") {
			flush()
			title := strings.TrimSpace(strings.TrimPrefix(line, "## "))
			cur = &docSection{num: leadingNum(title), title: title}
			continue
		}
		if cur != nil {
			body.WriteString(line)
			body.WriteByte('\n')
		}
	}
	flush()
	return out
}

// buildChunks splits each section at `### ` subheadings (fence-aware) into
// focused passages for query ranking, carrying a heading breadcrumb.
func buildChunks(sections []docSection) []docChunk {
	var out []docChunk
	for _, s := range sections {
		crumb := s.title
		var sub string
		var buf strings.Builder
		inFence := false
		emit := func() {
			text := strings.TrimSpace(buf.String())
			if text == "" {
				buf.Reset()
				return
			}
			h := crumb
			if sub != "" {
				h = crumb + " › " + sub
			}
			out = append(out, docChunk{heading: h, text: text})
			buf.Reset()
		}
		for _, line := range strings.Split(s.body, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "```") {
				inFence = !inFence
			}
			if !inFence && strings.HasPrefix(line, "### ") {
				emit()
				sub = strings.TrimSpace(strings.TrimPrefix(line, "### "))
				continue
			}
			buf.WriteString(line)
			buf.WriteByte('\n')
		}
		emit()
	}
	return out
}

// leadingNum parses the "12" from "12. Security model", else 0.
func leadingNum(title string) int {
	dot := strings.IndexByte(title, '.')
	if dot <= 0 {
		return 0
	}
	n := 0
	for _, r := range title[:dot] {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}

// TopicNames returns the stable topic names, in presentation order.
func TopicNames() []string {
	names := make([]string, len(topics))
	for i, t := range topics {
		names[i] = t.Name
	}
	return names
}

// TopicIndex renders a compact "name — description" list for the system prompt.
func TopicIndex() string {
	var b strings.Builder
	for _, t := range topics {
		fmt.Fprintf(&b, "- %s — %s\n", t.Name, t.Description)
	}
	return b.String()
}

// RenderTopic returns the concatenated section bodies for a topic, capped.
// Reports ok=false for an unknown topic.
func RenderTopic(name string) (string, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, t := range topics {
		if t.Name != name {
			continue
		}
		var b strings.Builder
		fmt.Fprintf(&b, "# forge: %s\n\n", t.Description)
		for _, n := range t.sections {
			if s, ok := sectionByNum[n]; ok {
				fmt.Fprintf(&b, "## %s\n\n%s\n\n", s.title, s.body)
			}
		}
		// create-skill also carries the dedicated skill-authoring guide.
		if t.Name == "create-skill" {
			for _, s := range skillBuilderSections {
				fmt.Fprintf(&b, "## %s\n\n%s\n\n", s.title, s.body)
			}
		}
		return capText(b.String(), maxDocChars), true
	}
	return "", false
}

// SearchDocs ranks doc chunks against a free-text query and returns the best
// passages (with breadcrumbs), capped. Falls back to the overview when nothing
// scores.
func SearchDocs(query string) string {
	terms := queryTerms(query)
	if len(terms) == 0 {
		s, _ := RenderTopic("overview")
		return s
	}
	type scored struct {
		c     docChunk
		score int
	}
	var ranked []scored
	for _, c := range knowledgeChunks {
		head := strings.ToLower(c.heading)
		text := strings.ToLower(c.text)
		s := 0
		for _, t := range terms {
			s += 3 * strings.Count(head, t) // heading hits weigh more
			s += strings.Count(text, t)
		}
		if s > 0 {
			ranked = append(ranked, scored{c, s})
		}
	}
	if len(ranked) == 0 {
		return fmt.Sprintf("No forge docs matched %q. Available topics: %s.",
			query, strings.Join(TopicNames(), ", "))
	}
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].score > ranked[j].score })

	var b strings.Builder
	for _, r := range ranked {
		block := fmt.Sprintf("## %s\n\n%s\n\n", r.c.heading, r.c.text)
		if b.Len()+len(block) > maxDocChars {
			break
		}
		b.WriteString(block)
	}
	if b.Len() == 0 { // single chunk larger than the cap
		return capText(fmt.Sprintf("## %s\n\n%s", ranked[0].c.heading, ranked[0].c.text), maxDocChars)
	}
	return b.String()
}

// queryTerms lowercases a query into de-duplicated significant tokens (len > 2).
func queryTerms(q string) []string {
	seen := map[string]bool{}
	var out []string
	// Split on any non-alphanumeric rune (so the predicate marks separators).
	isAlnum := func(r rune) bool {
		return (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
	}
	for _, f := range strings.FieldsFunc(strings.ToLower(q), func(r rune) bool {
		return !isAlnum(r)
	}) {
		if len(f) > 2 && !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	return out
}

func capText(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "\n\n… (truncated — ask a narrower question or a specific topic)"
}

// ForgeDocs is the retrieval dispatcher shared by the native tool and the MCP
// server. A query is ALWAYS honored (so a specific question isn't lost when a
// broad topic is also passed): when both a topic and a query are given, the
// query's search results come first (most relevant), followed by the topic.
func ForgeDocs(topic, query string) string {
	topic = strings.TrimSpace(topic)
	query = strings.TrimSpace(query)

	if topic != "" {
		doc, ok := RenderTopic(topic)
		if !ok {
			// Unknown topic → treat it as a query so the caller still gets help.
			if query == "" {
				query = topic
			}
			return SearchDocs(query)
		}
		if query == "" {
			return doc
		}
		// Both given: lead with the specific search, then the broad topic.
		return capText("## Search results for: "+query+"\n\n"+SearchDocs(query)+
			"\n\n---\n\n# Topic: "+topic+"\n\n"+doc, 2*maxDocChars)
	}
	return SearchDocs(query)
}

// forgeDocsInput is the JSON argument shape for the forge_docs tool.
type forgeDocsInput struct {
	Topic string `json:"topic,omitempty"`
	Query string `json:"query,omitempty"`
}

// ForgeDocsTool exposes ForgeDocs as a forge-core Tool for the native agent
// loop. The MCP server (Claude Code path) calls ForgeDocs directly.
type ForgeDocsTool struct{}

// Name implements tools.Tool.
func (ForgeDocsTool) Name() string { return "forge_docs" }

// Description implements tools.Tool.
func (ForgeDocsTool) Description() string {
	return "Look up authoritative Forge documentation. Pass `topic` for a full " +
		"section (" + strings.Join(TopicNames(), ", ") + "), or `query` for a " +
		"free-text search across all forge docs. Use this before answering how-to " +
		"questions or building an agent."
}

// Category implements tools.Tool.
func (ForgeDocsTool) Category() tools.Category { return tools.CategoryBuiltin }

// InputSchema implements tools.Tool.
func (ForgeDocsTool) InputSchema() json.RawMessage {
	names := make([]string, len(topics))
	for i, t := range topics {
		names[i] = `"` + t.Name + `"`
	}
	schema := `{
  "type": "object",
  "properties": {
    "topic": {"type": "string", "description": "A forge doc topic to return in full.", "enum": [` + strings.Join(names, ", ") + `]},
    "query": {"type": "string", "description": "Free-text question to search the forge docs for."}
  }
}`
	return json.RawMessage(schema)
}

// Execute implements tools.Tool.
func (ForgeDocsTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var in forgeDocsInput
	if len(args) > 0 {
		if err := json.Unmarshal(args, &in); err != nil {
			return "", fmt.Errorf("forge_docs: invalid arguments: %w", err)
		}
	}
	if in.Topic == "" && in.Query == "" {
		return "Specify `topic` (" + strings.Join(TopicNames(), ", ") + ") or `query`.", nil
	}
	return ForgeDocs(in.Topic, in.Query), nil
}
