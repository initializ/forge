package msteams

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/initializ/forge/forge-plugins/channels/markdown"
)

// graphClient wraps the Microsoft Graph HTTP API. It is constructed with an
// authoriser (authManager) and a base URL (overridable for sovereign clouds
// and tests). All requests go through a single helper that handles the
// classed error responses described in the issue (401/403/410/429/5xx).
type graphClient struct {
	baseURL string
	client  *http.Client
	auth    *authManager
}

// Sentinel errors returned by the request helper. The poll loop pattern-matches
// on these to drive backoff / cursor reset / token refresh strategies.
var (
	errUnauthorized  = errors.New("msteams graph: 401 unauthorized")
	errForbidden     = errors.New("msteams graph: 403 forbidden")
	errCursorExpired = errors.New("msteams graph: 410 gone (delta cursor expired)")
	errRateLimited   = errors.New("msteams graph: 429 rate limited")
)

// rateLimitedError wraps errRateLimited with the Retry-After hint.
type rateLimitedError struct {
	RetryAfter time.Duration
}

func (e *rateLimitedError) Error() string {
	return fmt.Sprintf("msteams graph: 429 rate limited (retry after %s)", e.RetryAfter)
}
func (e *rateLimitedError) Unwrap() error { return errRateLimited }

func newGraphClient(baseURL string, client *http.Client, auth *authManager) *graphClient {
	if baseURL == "" {
		baseURL = "https://graph.microsoft.com/v1.0"
	}
	if client == nil {
		client = &http.Client{Timeout: 360 * time.Second}
	}
	return &graphClient{baseURL: baseURL, client: client, auth: auth}
}

// MeResponse is the trimmed Graph /me payload — only the fields the
// adapter caches at startup.
type MeResponse struct {
	ID                string `json:"id"`
	DisplayName       string `json:"displayName"`
	UserPrincipalName string `json:"userPrincipalName"`
}

// Me fetches the authenticated user's identity. Used in delegated flow.
func (g *graphClient) Me(ctx context.Context) (*MeResponse, error) {
	var out MeResponse
	if err := g.getJSON(ctx, "/me", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// User fetches a specific user by id. Used in client_credentials flow where
// /me has no meaning.
func (g *graphClient) User(ctx context.Context, userID string) (*MeResponse, error) {
	var out MeResponse
	if err := g.getJSON(ctx, "/users/"+url.PathEscape(userID), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ChatMessage is the trimmed Graph chatMessage representation the adapter
// cares about. Fields not modelled here decode silently.
type ChatMessage struct {
	ID                   string `json:"id"`
	ChatID               string `json:"chatId"`
	ChannelIdentity      any    `json:"channelIdentity,omitempty"` // present for team-channel messages; we ignore them
	ChatType             string `json:"chatType,omitempty"`        // sometimes inlined; usually fetched separately
	CreatedDateTime      string `json:"createdDateTime"`
	LastModifiedDateTime string `json:"lastModifiedDateTime"`
	Subject              string `json:"subject,omitempty"`
	MessageType          string `json:"messageType,omitempty"`
	Importance           string `json:"importance,omitempty"`
	From                 *struct {
		User *struct {
			ID               string `json:"id"`
			DisplayName      string `json:"displayName"`
			UserIdentityType string `json:"userIdentityType,omitempty"`
			TenantID         string `json:"tenantId,omitempty"`
		} `json:"user,omitempty"`
		Application *struct {
			ID                  string `json:"id"`
			DisplayName         string `json:"displayName"`
			ApplicationIdentity string `json:"applicationIdentityType,omitempty"`
		} `json:"application,omitempty"`
	} `json:"from,omitempty"`
	Body struct {
		ContentType string `json:"contentType"` // "html" or "text"
		Content     string `json:"content"`
	} `json:"body"`
	Mentions []markdown.TeamsMention `json:"mentions,omitempty"`
}

// DeltaPage is one page of a getAllMessages/delta response. Exactly one of
// NextLink or DeltaLink is set per page; the poll loop continues paging
// until DeltaLink appears, then persists it.
type DeltaPage struct {
	Messages  []ChatMessage `json:"value"`
	NextLink  string        `json:"@odata.nextLink,omitempty"`
	DeltaLink string        `json:"@odata.deltaLink,omitempty"`
}

// InitialDeltaURL builds the first delta URL for a fresh adapter using the
// APP-ONLY /users/{id}/chats/getAllMessages/delta endpoint. This API
// requires Chat.Read.All (application) — delegated tokens return
// HTTP 412 PreconditionFailed with "Requested API is not supported in
// delegated context". Use this when the adapter is configured with
// auth_flow: client_credentials.
//
// For delegated flow, use ListChats + InitialChatDeltaURL instead.
func (g *graphClient) InitialDeltaURL(userID string, since time.Time) string {
	// Microsoft Graph requires an exact ISO-8601 timestamp.
	filter := "lastModifiedDateTime gt " + since.UTC().Format(time.RFC3339)
	v := url.Values{}
	v.Set("$filter", filter)
	return fmt.Sprintf("%s/users/%s/chats/getAllMessages/delta?%s",
		g.baseURL, url.PathEscape(userID), v.Encode())
}

// ChatRef is the minimal shape returned by GET /me/chats / /chats — enough
// to drive per-chat polling. ChatType discriminates oneOnOne/group/meeting.
type ChatRef struct {
	ID       string `json:"id"`
	Topic    string `json:"topic,omitempty"`
	ChatType string `json:"chatType"`
}

type chatsResponse struct {
	Value    []ChatRef `json:"value"`
	NextLink string    `json:"@odata.nextLink,omitempty"`
}

// ListChats enumerates the authenticated user's chats. Used by the delegated
// polling path so we can fan out per-chat delta cursors (the
// getAllMessages/delta API is app-only). Returns at most `limit` chats; if
// limit <= 0 a sensible default of 50 is used. Pagination via @odata.nextLink
// is followed transparently up to the limit.
func (g *graphClient) ListChats(ctx context.Context, limit int) ([]ChatRef, error) {
	if limit <= 0 {
		limit = 50
	}
	page := limit
	if page > 50 {
		// Graph's per-request cap for /me/chats is 50; bigger requests
		// would 400. Stay under the cap and follow nextLink.
		page = 50
	}

	v := url.Values{}
	v.Set("$select", "id,topic,chatType")
	v.Set("$top", fmt.Sprintf("%d", page))
	next := fmt.Sprintf("%s/me/chats?%s", g.baseURL, v.Encode())

	var out []ChatRef
	for next != "" && len(out) < limit {
		var resp chatsResponse
		if err := g.getAbsoluteJSON(ctx, next, &resp); err != nil {
			return nil, err
		}
		out = append(out, resp.Value...)
		next = resp.NextLink
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// InitialChatDeltaURL builds the first delta URL for a single chat in the
// delegated flow. /chats/{id}/messages/delta supports delegated context
// (Chat.Read or ChatMessage.Read) and is the foundation of the
// per-chat polling design.
func (g *graphClient) InitialChatDeltaURL(chatID string, since time.Time) string {
	filter := "lastModifiedDateTime gt " + since.UTC().Format(time.RFC3339)
	v := url.Values{}
	v.Set("$filter", filter)
	return fmt.Sprintf("%s/chats/%s/messages/delta?%s",
		g.baseURL, url.PathEscape(chatID), v.Encode())
}

// FetchDeltaPage retrieves the next page of messages. The URL is the full
// @odata.nextLink or @odata.deltaLink from the previous response, or the
// result of InitialDeltaURL on first call.
func (g *graphClient) FetchDeltaPage(ctx context.Context, pageURL string) (*DeltaPage, error) {
	var out DeltaPage
	if err := g.getAbsoluteJSON(ctx, pageURL, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// PostChatMessage posts an HTML-body message to a chat.
func (g *graphClient) PostChatMessage(ctx context.Context, chatID, contentHTML string) error {
	body := map[string]any{
		"body": map[string]any{
			"contentType": "html",
			"content":     contentHTML,
		},
	}
	return g.postJSON(ctx, fmt.Sprintf("/chats/%s/messages", url.PathEscape(chatID)), body, nil)
}

// PostChatMessageWithAttachment posts a chat message that references a
// hosted-content attachment. Used for large responses (>24 KB HTML body).
// Per the issue: hosted-contents have a 4 MB cap; callers must fall back to
// chunked text beyond that.
func (g *graphClient) PostChatMessageWithAttachment(ctx context.Context, chatID, filename, mimeType string, content []byte) error {
	if len(content) > 4*1024*1024 {
		return fmt.Errorf("msteams graph: attachment too large (%d bytes; limit 4 MiB)", len(content))
	}
	contentID := fmt.Sprintf("forge-%d", time.Now().UnixNano())
	body := map[string]any{
		"body": map[string]any{
			"contentType": "html",
			"content":     fmt.Sprintf(`<attachment id="%s"></attachment>`, contentID),
		},
		"attachments": []map[string]any{{
			"id":          contentID,
			"contentType": "reference",
			"contentUrl":  fmt.Sprintf("data:%s;base64,%s", mimeType, base64.StdEncoding.EncodeToString(content)),
			"name":        filename,
		}},
	}
	return g.postJSON(ctx, fmt.Sprintf("/chats/%s/messages", url.PathEscape(chatID)), body, nil)
}

// getJSON issues a GET against a relative path under baseURL.
func (g *graphClient) getJSON(ctx context.Context, path string, out any) error {
	return g.getAbsoluteJSON(ctx, g.baseURL+path, out)
}

// getAbsoluteJSON issues a GET against a fully-qualified URL (used for
// @odata.nextLink / @odata.deltaLink which embed the full host).
func (g *graphClient) getAbsoluteJSON(ctx context.Context, fullURL string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
	if err != nil {
		return fmt.Errorf("msteams graph: build request: %w", err)
	}
	return g.doJSON(req, out)
}

func (g *graphClient) postJSON(ctx context.Context, path string, body, out any) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("msteams graph: marshal body: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.baseURL+path, bytes.NewReader(buf))
	if err != nil {
		return fmt.Errorf("msteams graph: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	return g.doJSON(req, out)
}

// doJSON runs the request, applies the bearer token, classifies the error,
// and decodes the response body into out (if non-nil).
func (g *graphClient) doJSON(req *http.Request, out any) error {
	token, err := g.auth.Token(req.Context())
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := g.client.Do(req)
	if err != nil {
		return fmt.Errorf("msteams graph: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated, http.StatusAccepted:
		if out == nil {
			return nil
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 16*1024*1024))
		if err != nil {
			return fmt.Errorf("msteams graph: read body: %w", err)
		}
		if jerr := json.Unmarshal(body, out); jerr != nil {
			return fmt.Errorf("msteams graph: decode body: %w", jerr)
		}
		return nil
	case http.StatusUnauthorized:
		return errUnauthorized
	case http.StatusForbidden:
		return errForbidden
	case http.StatusGone:
		return errCursorExpired
	case http.StatusTooManyRequests:
		retry := parseRetryAfter(resp.Header.Get("Retry-After"))
		return &rateLimitedError{RetryAfter: retry}
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
		return fmt.Errorf("msteams graph: status %d: %s", resp.StatusCode, string(body))
	}
}

// parseRetryAfter accepts either an integer seconds value or an HTTP-date.
// Falls back to 10s if unparsable (the issue's documented minimum backoff).
func parseRetryAfter(h string) time.Duration {
	if h == "" {
		return 10 * time.Second
	}
	if n, err := strconv.Atoi(h); err == nil && n > 0 {
		d := time.Duration(n) * time.Second
		if d < 10*time.Second {
			return 10 * time.Second
		}
		if d > 300*time.Second {
			return 300 * time.Second
		}
		return d
	}
	if t, err := http.ParseTime(h); err == nil {
		d := time.Until(t)
		if d < 10*time.Second {
			return 10 * time.Second
		}
		if d > 300*time.Second {
			return 300 * time.Second
		}
		return d
	}
	return 10 * time.Second
}
