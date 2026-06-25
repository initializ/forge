package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/initializ/forge/forge-cli/runtime"
)

// guardrailsCmd groups the operator-facing commands for the
// MongoDB-backed guardrails subsystem. The runtime selection between
// DB mode and file mode is documented in docs/security/guardrails.md;
// these subcommands help operators seed a fresh AgentConfig and
// validate an existing one. Issue #166.
var guardrailsCmd = &cobra.Command{
	Use:   "guardrails",
	Short: "Manage guardrails configuration (DB mode operator helpers)",
	Long: `Operator helpers for the MongoDB-backed guardrails subsystem.

Forge can run guardrails in two mutually-exclusive modes:

  - File mode (default): reads guardrails.json from the agent's
    workdir; falls back to built-in DefaultStructuredGuardrails when
    the file is absent.
  - DB mode (FORGE_GUARDRAILS_DB set): loads AgentConfig from
    MongoDB. The library reads the document on every gate call.

In DB mode the operator's MongoDB seed is the ONLY source of policy —
the built-in DefaultStructuredGuardrails is NOT applied. An incomplete
seed (missing PII config, no secret-pattern rules, no jailbreak
threshold) leaves the agent strictly less protected than a file-mode
deploy with no file at all. These subcommands help operators avoid
that footgun.

See docs/security/guardrails.md for the full resolution ladder.`,
}

var guardrailsSeedDefaultsCmd = &cobra.Command{
	Use:   "seed-defaults",
	Short: "Print DefaultStructuredGuardrails as JSON suitable for MongoDB seeding",
	Long: `Print the built-in DefaultStructuredGuardrails as pretty-printed JSON.

The output matches the library's models.StructuredGuardrails schema
and is suitable for loading directly into MongoDB as the agent's
AgentConfig.structured_guardrails document. Pipe to a file and edit
before seeding if you want to tweak thresholds or add rules.

  forge guardrails seed-defaults > agent-config.json

The built-in defaults cover 11 vendor-secret patterns (Anthropic /
OpenAI / GitHub / AWS / Slack / Telegram tokens, private-key PEM
blocks), four PII categories (email / phone / SSN / credit card), and
jailbreak / prompt-injection / command-injection thresholds. DB-mode
deploys SHOULD use this as their seed baseline; an empty AgentConfig
document is strictly less protected than file mode with no file.`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		sg := runtime.DefaultStructuredGuardrails()
		out, err := json.MarshalIndent(sg, "", "  ")
		if err != nil {
			return fmt.Errorf("marshaling defaults: %w", err)
		}
		// Trailing newline so shell redirection produces a
		// well-formed file. cobra writes to OutOrStdout so tests
		// can capture stdout via SetOut.
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), string(out))
		return nil
	},
}

// guardrailsValidateDBOpts holds the resolved connection settings for
// `forge guardrails validate-db`. Defaults pulled from env so a CI
// run with the deployment env set "just works."
type guardrailsValidateDBOpts struct {
	mongoURI  string
	agentID   string
	dbName    string
	colName   string
	timeoutMs int
}

var validateDBOpts guardrailsValidateDBOpts

var guardrailsValidateDBCmd = &cobra.Command{
	Use:   "validate-db",
	Short: "Connect to FORGE_GUARDRAILS_DB and report on the agent's seeded config",
	Long: `Connect to MongoDB and inspect the agent's AgentConfig document.

Reports on baseline coverage:
  - PII config present and enabled
  - JailbreakDetection / PromptInjection / CommandInjection thresholds
  - Number of custom secret-pattern rules
  - Gate config (input / output / tool_call gates enabled)

Warns when coverage is below the built-in defaults (the threshold for
"reasonably configured" is fewer than 5 secret-pattern rules, missing
PII config, or missing jailbreak detection — these are common signs
of an incomplete or stale seed).

Connection settings default to env:
  --mongo-uri  $FORGE_GUARDRAILS_DB
  --agent-id   $FORGE_AGENT_ID
  --db         Initializ
  --collection AgentConfig

Exits with status 1 when the agent has no document at all (the most
common deploy error) so CI / deployment hooks can fail the rollout.`,
	RunE: runGuardrailsValidateDB,
}

func init() {
	guardrailsCmd.AddCommand(guardrailsSeedDefaultsCmd)
	guardrailsCmd.AddCommand(guardrailsValidateDBCmd)
	guardrailsValidateDBCmd.Flags().StringVar(&validateDBOpts.mongoURI, "mongo-uri", "",
		"MongoDB URI (default: $FORGE_GUARDRAILS_DB)")
	guardrailsValidateDBCmd.Flags().StringVar(&validateDBOpts.agentID, "agent-id", "",
		"agent_id to look up (default: $FORGE_AGENT_ID)")
	guardrailsValidateDBCmd.Flags().StringVar(&validateDBOpts.dbName, "db", "Initializ",
		"MongoDB database name")
	guardrailsValidateDBCmd.Flags().StringVar(&validateDBOpts.colName, "collection", "AgentConfig",
		"AgentConfig collection name")
	guardrailsValidateDBCmd.Flags().IntVar(&validateDBOpts.timeoutMs, "timeout-ms", 5000,
		"connect timeout in milliseconds")
}

// runGuardrailsValidateDB is the validate-db handler. Returning an
// error from RunE makes cobra exit non-zero; the CI signal is the
// whole point of the command.
func runGuardrailsValidateDB(cmd *cobra.Command, _ []string) error {
	uri := validateDBOpts.mongoURI
	if uri == "" {
		uri = os.Getenv(runtime.EnvGuardrailsDB)
	}
	if uri == "" {
		return errors.New("no MongoDB URI: pass --mongo-uri or set FORGE_GUARDRAILS_DB")
	}
	agentID := validateDBOpts.agentID
	if agentID == "" {
		agentID = os.Getenv("FORGE_AGENT_ID")
	}
	if agentID == "" {
		return errors.New("no agent_id: pass --agent-id or set FORGE_AGENT_ID")
	}

	timeout := time.Duration(validateDBOpts.timeoutMs) * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	client, err := mongo.Connect(ctx, options.Client().ApplyURI(uri))
	if err != nil {
		return fmt.Errorf("mongo connect: %w", err)
	}
	defer func() { _ = client.Disconnect(context.Background()) }()

	if err := client.Ping(ctx, nil); err != nil {
		return fmt.Errorf("mongo ping: %w", err)
	}

	col := client.Database(validateDBOpts.dbName).Collection(validateDBOpts.colName)
	var doc bson.M
	if err := col.FindOne(ctx, bson.M{"agent_id": agentID}).Decode(&doc); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return fmt.Errorf("no AgentConfig document for agent_id=%q in %s.%s; seed it via `forge guardrails seed-defaults | mongoimport`",
				agentID, validateDBOpts.dbName, validateDBOpts.colName)
		}
		return fmt.Errorf("fetching AgentConfig: %w", err)
	}

	report := scoreAgentConfig(doc)
	out := cmd.OutOrStdout()
	_, _ = fmt.Fprintf(out, "AgentConfig for %q in %s.%s\n", agentID, validateDBOpts.dbName, validateDBOpts.colName)
	for _, line := range report.lines {
		_, _ = fmt.Fprintln(out, "  "+line)
	}
	if len(report.warnings) > 0 {
		_, _ = fmt.Fprintln(out, "\nWARNINGS:")
		for _, w := range report.warnings {
			_, _ = fmt.Fprintln(out, "  - "+w)
		}
		_, _ = fmt.Fprintln(out, "\nReseed with `forge guardrails seed-defaults` to restore baseline coverage.")
	} else {
		_, _ = fmt.Fprintln(out, "\nOK — baseline coverage matches DefaultStructuredGuardrails.")
	}
	return nil
}

// agentConfigReport summarizes a validate-db inspection in a form
// the test can assert on cleanly. Lines are the human-readable
// summary; warnings are the actionable below-baseline findings.
type agentConfigReport struct {
	lines    []string
	warnings []string
}

// scoreAgentConfig walks the BSON document and reports coverage. The
// "fewer than 5 secret rules / missing PII / missing jailbreak"
// threshold is the issue #166 baseline — anything below that is
// strictly less protective than the built-in defaults and the
// operator probably didn't intend it.
//
// Forgiving on shape: missing nested objects are reported as missing
// (not an error). The library's struct tags use camelCase, but
// historical seeds in the wild may use snake_case (older library
// versions or hand-written seeds based on docs). lookupMap accepts
// either spelling so the validator works against both shapes.
func scoreAgentConfig(doc bson.M) agentConfigReport {
	r := agentConfigReport{}

	// Custom rules — secret-pattern coverage.
	rules := extractCustomRules(doc)
	r.lines = append(r.lines, fmt.Sprintf("custom_rules: %d rule(s)", len(rules)))
	secretRules := 0
	for _, name := range rules {
		// Same convention DefaultStructuredGuardrails uses.
		if len(name) >= 6 && name[:6] == "secret" {
			secretRules++
		}
	}
	r.lines = append(r.lines, fmt.Sprintf("secret_pattern_rules: %d (default seed ships 11)", secretRules))
	if secretRules < 5 {
		r.warnings = append(r.warnings,
			fmt.Sprintf("fewer than 5 secret-pattern rules (%d found) — vendor token leakage in outbound responses likely unmasked", secretRules))
	}

	// PII config.
	if pii, ok := lookupMap(doc, "pii"); ok {
		enabled, _ := pii["enabled"].(bool)
		r.lines = append(r.lines, fmt.Sprintf("pii.enabled: %v", enabled))
		if !enabled {
			r.warnings = append(r.warnings, "PII config present but disabled — email / phone / SSN / credit card in prompts will not be masked")
		}
	} else {
		r.lines = append(r.lines, "pii: <missing>")
		r.warnings = append(r.warnings, "no PII config — PII in prompts will not be masked")
	}

	// Security thresholds. Walk both camelCase (library struct
	// tags) and snake_case (legacy seeds).
	if sec, ok := lookupMap(doc, "security"); ok {
		for _, names := range [][]string{
			{"jailbreakDetection", "jailbreak_detection"},
			{"promptInjection", "prompt_injection"},
			{"commandInjection", "command_injection"},
		} {
			label := names[1]
			t, ok := lookupMap(sec, names...)
			if ok {
				enabled, _ := t["enabled"].(bool)
				r.lines = append(r.lines, fmt.Sprintf("security.%s.enabled: %v", label, enabled))
				if !enabled {
					r.warnings = append(r.warnings, fmt.Sprintf("security.%s present but disabled", label))
				}
			} else {
				r.lines = append(r.lines, fmt.Sprintf("security.%s: <missing>", label))
				r.warnings = append(r.warnings, fmt.Sprintf("security.%s missing — this attack class is not screened", label))
			}
		}
	} else {
		r.lines = append(r.lines, "security: <missing>")
		r.warnings = append(r.warnings,
			"no security config — jailbreak / prompt-injection / command-injection detection is OFF")
	}

	// Gate config.
	if gc, ok := lookupMap(doc, "gateConfig", "gate_config"); ok {
		input, _ := lookupBool(gc, "inputGate", "input_gate")
		output, _ := lookupBool(gc, "outputGate", "output_gate")
		toolCall, _ := lookupBool(gc, "toolCallGate", "tool_call_gate")
		r.lines = append(r.lines, fmt.Sprintf("gates: input=%v output=%v tool_call=%v", input, output, toolCall))
		if !input || !output || !toolCall {
			r.warnings = append(r.warnings,
				"one or more core gates disabled (input / output / tool_call) — partial enforcement")
		}
	} else {
		r.lines = append(r.lines, "gate_config: <missing>")
		r.warnings = append(r.warnings, "no gate_config — falling back to library defaults")
	}

	return r
}

// extractCustomRules pulls the rule ids out of the
// `customRules.rules` array (with snake_case fallback), defensive
// against shape drift. Returns empty when the path is absent or any
// node along the way is the wrong type.
func extractCustomRules(doc bson.M) []string {
	cr, ok := lookupMap(doc, "customRules", "custom_rules")
	if !ok {
		return nil
	}
	rawRules, ok := cr["rules"].(bson.A)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(rawRules))
	for _, r := range rawRules {
		rule, ok := r.(bson.M)
		if !ok {
			continue
		}
		if id, ok := rule["id"].(string); ok {
			out = append(out, id)
			continue
		}
		if name, ok := rule["name"].(string); ok {
			out = append(out, name)
		}
	}
	return out
}

// lookupMap returns the first BSON map child of doc that matches one
// of the supplied keys. Used to tolerate both camelCase (library
// struct tags) and snake_case (older seed conventions) without
// duplicating each lookup.
func lookupMap(doc bson.M, keys ...string) (bson.M, bool) {
	for _, k := range keys {
		if v, ok := doc[k].(bson.M); ok {
			return v, true
		}
	}
	return nil, false
}

// lookupBool is the bool-leaf counterpart of lookupMap.
func lookupBool(doc bson.M, keys ...string) (bool, bool) {
	for _, k := range keys {
		if v, ok := doc[k].(bool); ok {
			return v, true
		}
	}
	return false, false
}
