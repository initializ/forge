// Package channels defines the channel adapter architecture for exposing
// self-hosted agents via messaging platforms like Slack and Telegram.
package channels

import (
	"context"
	"encoding/json"
	"time"

	"github.com/initializ/forge/forge-core/a2a"
)

// ChannelPlugin is the interface every channel adapter must implement.
type ChannelPlugin interface {
	// Name returns the adapter name (e.g. "slack", "telegram").
	Name() string
	// Init configures the plugin from a ChannelConfig.
	Init(cfg ChannelConfig) error
	// Start begins listening for events and dispatching them to handler.
	// It blocks until ctx is cancelled.
	Start(ctx context.Context, handler EventHandler) error
	// Stop gracefully shuts down the plugin.
	Stop() error
	// NormalizeEvent converts raw platform bytes into a ChannelEvent.
	NormalizeEvent(raw []byte) (*ChannelEvent, error)
	// SendResponse delivers an A2A response back to the originating platform.
	SendResponse(event *ChannelEvent, response *a2a.Message) error
}

// EventHandler is the callback signature provided by the router.
// The plugin calls it when a message arrives; the handler forwards the event
// to the A2A server and returns the agent's response.
type EventHandler func(ctx context.Context, event *ChannelEvent) (*a2a.Message, error)

// ChannelConfig holds per-adapter configuration loaded from YAML.
type ChannelConfig struct {
	Adapter     string            `yaml:"adapter"`
	WebhookPort int               `yaml:"webhook_port,omitempty"`
	WebhookPath string            `yaml:"webhook_path,omitempty"`
	Settings    map[string]string `yaml:"settings,omitempty"`
}

// ChannelEvent is the normalized representation of an inbound message
// from any supported platform.
type ChannelEvent struct {
	Channel     string          `json:"channel"`
	WorkspaceID string          `json:"workspace_id"`
	UserID      string          `json:"user_id"`
	ThreadID    string          `json:"thread_id,omitempty"`
	MessageID   string          `json:"message_id,omitempty"` // per-message ID for reply targeting
	Message     string          `json:"message"`
	Attachments []Attachment    `json:"attachments,omitempty"`
	Raw         json.RawMessage `json:"raw,omitempty"`
}

// Attachment represents a file or media item attached to a channel message.
type Attachment struct {
	Name     string `json:"name,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
	URL      string `json:"url,omitempty"`
}

// --- Interactive human-approval (DEFER / R4c, #211) delivery -----------------

// ApprovalRequest is a pending human-approval to deliver to an approver via an
// interactive channel message (e.g. Slack Block Kit buttons). The runtime
// builds one when a tool call is deferred; a channel adapter renders it.
type ApprovalRequest struct {
	TaskID  string        // the deferred A2A task; the resolution key
	Tool    string        // the tool call awaiting approval
	Context string        // rendered context_template (what the agent wants to do)
	Timeout time.Duration // how long until auto-deny
	Target  string        // adapter-specific destination (e.g. a Slack channel "#oncall")
}

// ApprovalDecision is an approver's response, delivered back to the runtime by
// the adapter that received the interaction.
type ApprovalDecision struct {
	TaskID   string // must match the ApprovalRequest.TaskID
	Decision string // "approve" | "reject"
	Approver string // who acted (platform user id / name), for audit
	// ApproverEmail is the approver's resolved email (#313). Adapters populate
	// it (e.g. Slack via users.info) so the runtime's approver allowlist check
	// is adapter-agnostic. Empty when the adapter couldn't resolve it — the
	// runtime fails closed against a configured allowlist.
	ApproverEmail string
	Note          string // optional justification
}

// ApprovalResolver is invoked by an adapter when an approver acts on a
// delivered ApprovalRequest. The runtime routes it to the deferred task's
// decision (typically POST /tasks/{id}/decisions). Wired via
// ApprovalDeliverer.SetApprovalResolver at startup.
type ApprovalResolver func(ctx context.Context, d ApprovalDecision) error

// Logger is the minimal structured ops logger a channel adapter uses for
// operational signals (the FWS-9 stdout ops stream). Satisfied by
// forge-core/runtime.Logger, so the runtime can wire its logger without this
// package importing it.
type Logger interface {
	Info(msg string, fields map[string]any)
	Warn(msg string, fields map[string]any)
	Error(msg string, fields map[string]any)
	Debug(msg string, fields map[string]any)
}

// LoggerAware is an OPTIONAL capability: an adapter that routes operational
// signals through a structured logger implements it. The runtime wires it at
// startup; adapters that don't implement it keep their own logging.
type LoggerAware interface {
	SetLogger(Logger)
}

// ApprovalDeliverer is an OPTIONAL capability. A channel adapter that can post
// an interactive approval request AND receive the approver's response
// implements it (Slack via Block Kit over Socket Mode, #310). Adapters that
// don't implement it simply can't be a DEFER `to:` target; the deferral still
// works via a direct POST /tasks/{id}/decisions.
type ApprovalDeliverer interface {
	// DeliverApproval posts the interactive approval request to req.Target.
	DeliverApproval(ctx context.Context, req ApprovalRequest) error
	// SetApprovalResolver wires the callback the adapter invokes when an
	// approver acts. The runtime sets this once at startup.
	SetApprovalResolver(r ApprovalResolver)
}

// --- MCP delegated-consent (#343) delivery -----------------------------------

// ConsentPrompt is a pending MCP delegated-consent prompt: a "Connect <server>"
// login link to present to the requesting user so they can authorize the agent
// to act as them. The runtime builds one when a type: user MCP call parks on
// the auth-required gate (#330); a channel adapter presents it.
//
// Delivery is independent of who built the URL and who hosts the callback:
// AuthorizeURL is opaque to the adapter (standalone → Forge-built; managed →
// platform-supplied), so the same delivery code serves both modes.
type ConsentPrompt struct {
	Subject      string         // the requesting user (email preferred) — who to reach
	Server       string         // MCP server name — for the prompt copy
	AuthorizeURL string         // the login link the user opens (a URL button)
	Deadline     time.Time      // the gate timeout — rendered as "expires …"
	Origin       *ChannelOrigin // optional: reply in the origin thread if the request came via this channel
}

// ChannelOrigin locates where a request came from so consent can be presented
// where the user is already talking (an in-thread reply) rather than a cold DM.
// An adapter populates it on inbound; nil ⇒ the deliverer falls back to
// reaching the Subject directly (e.g. Slack DM by email).
type ChannelOrigin struct {
	Adapter  string // e.g. "slack"
	Channel  string // native channel / DM id
	ThreadTS string // thread to reply in (optional)
	UserID   string // native user id (skips an email lookup)
}

// ConsentCanceler is invoked by an adapter when the user cancels a delivered
// consent prompt (e.g. a "Cancel" button). The runtime fails the parked call
// fast instead of idling to the deadline. Wired via
// ConsentDeliverer.SetConsentCanceler at startup; optional.
type ConsentCanceler func(ctx context.Context, subject, server string) error

// ConsentDeliverer is an OPTIONAL capability. A channel adapter that can present
// an MCP consent login link to a specific user implements it (Slack via a DM /
// in-thread Block Kit message, #343). Adapters that don't implement it simply
// can't deliver consent prompts; the runtime falls back to publishing the link
// on the A2A auth-required artifact.
type ConsentDeliverer interface {
	// DeliverConsent presents the consent prompt to req.Subject (or req.Origin).
	DeliverConsent(ctx context.Context, req ConsentPrompt) error
	// SetConsentCanceler wires the callback the adapter invokes when the user
	// cancels. The runtime sets this once at startup; may be a no-op.
	SetConsentCanceler(c ConsentCanceler)
}
