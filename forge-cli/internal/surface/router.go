package surface

import "strings"

// topicAliases maps lowercase keyword/alias substrings to a topic name. The
// native-path router scans a turn's input for these and injects the matched
// topic module(s) once per session. Order within a topic doesn't matter; the
// first matching alias wins per topic.
var topicAliases = map[string][]string{
	"channels":     {"channel", "slack", "telegram", "whatsapp", "msteams", "ms teams", "teams", "adapter"},
	"mcp":          {"mcp", "model context protocol", "oauth server", "tool server"},
	"forge-yaml":   {"forge.yaml", "yaml", "config", "egress", "allowed_domains", "provider block", "model block"},
	"models":       {"provider", "model", "anthropic", "openai", "ollama", "bedrock", "gemini", "credential", "api key", "api_key"},
	"tools":        {"tool", "builtin", "http_request", "web_fetch", "web_search", "function call"},
	"create-skill": {"skill", "skill.md", "write a skill", "author a skill", "create a skill", "new skill", "skill builder", "import", "from-skill-dir", "convert", "anthropic skill", "existing skill"},
	"create-agent": {"create an agent", "scaffold", "new agent", "build an agent", "make an agent"},
	"scheduling":   {"schedule", "cron", "recurring", "every day", "daily", "interval"},
	"secrets":      {"secret", "encrypt", "passphrase", "credential store"},
	"memory":       {"memory", "remember", "recall", "vector", "embedding"},
	"security":     {"security", "egress", "guardrail", "defer", "approval", "policy", "audit"},
	"build-deploy": {"deploy", "kubernetes", "k8s", "package", "container", "docker", "image", "build"},
	"cli":          {"command", "subcommand", "cli", "forge run", "forge build"},
	"audit":        {"audit", "ndjson", "hash chain", "event"},
	"how-it-works": {"a2a", "how does forge", "end-to-end", "executor", "runtime loop"},
	"recipes":      {"recipe", "how do i", "example"},
}

// aliasOrder pins deterministic topic scan order (map iteration is random), most
// specific topics first so, e.g., "write a skill" prefers create-skill over the
// broad skills topic.
var aliasOrder = []string{
	"create-skill", "create-agent", "channels", "mcp", "forge-yaml", "models",
	"tools", "scheduling", "secrets", "memory", "security",
	"build-deploy", "audit", "how-it-works", "cli", "recipes",
}

// Router selects knowledge topics by intent and injects each at most once per
// session, so context grows bounded (after the first injection the module is
// already in history).
type Router struct {
	injected map[string]bool
}

// NewRouter builds a per-session intent router.
func NewRouter() *Router { return &Router{injected: map[string]bool{}} }

// Match returns the topics whose aliases appear in the input and that have not
// yet been injected this session, marking them injected. At most two topics per
// turn keeps the injected block small.
func (r *Router) Match(input string) []string {
	in := strings.ToLower(input)
	var out []string
	for _, topic := range aliasOrder {
		if r.injected[topic] {
			continue
		}
		for _, alias := range topicAliases[topic] {
			if strings.Contains(in, alias) {
				r.injected[topic] = true
				out = append(out, topic)
				break
			}
		}
		if len(out) >= 2 {
			break
		}
	}
	return out
}

// KnowledgePreface renders the matched topics as a single delimited block to
// prepend to a turn's input. Returns "" when nothing new matched.
func (r *Router) KnowledgePreface(input string) string {
	matched := r.Match(input)
	if len(matched) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("[forge knowledge — reference for this turn]\n\n")
	for _, topic := range matched {
		if doc, ok := RenderTopic(topic); ok {
			b.WriteString(doc)
			b.WriteString("\n\n")
		}
	}
	b.WriteString("[end forge knowledge]\n\n")
	return b.String()
}
