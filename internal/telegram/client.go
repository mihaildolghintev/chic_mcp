// Package telegram is a minimal Telegram Bot API client and webhook receiver.
// It covers exactly what the bot needs (messages in, text out, webhook
// registration, file metadata) with no third-party dependencies.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// DefaultBaseURL is the Telegram Bot API root. The bot token is appended per
// request: <base>/bot<token>/<method>.
const DefaultBaseURL = "https://api.telegram.org"

// Client is a Telegram Bot API client. It is safe for concurrent use.
type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

// Option configures a Client.
type Option func(*Client)

// WithBaseURL overrides the API root (used to point tests at httptest).
func WithBaseURL(u string) Option { return func(c *Client) { c.baseURL = u } }

// WithHTTPClient injects a custom *http.Client.
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.httpClient = h } }

// NewClient builds a Client authenticated with a bot token from @BotFather.
func NewClient(token string, opts ...Option) *Client {
	c := &Client{
		baseURL:    DefaultBaseURL,
		token:      token,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// apiResponse is the envelope every Bot API method returns.
type apiResponse struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	Description string          `json:"description"`
	ErrorCode   int             `json:"error_code"`
}

// call POSTs a JSON payload to a Bot API method and decodes the result into
// out (which may be nil when the caller only cares about success).
func (c *Client) call(ctx context.Context, method string, payload, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("telegram %s: marshal: %w", method, err)
	}
	url := c.baseURL + "/bot" + c.token + "/" + method
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("telegram %s: %w", method, err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("telegram %s: %w", method, err)
	}
	defer func() { _ = resp.Body.Close() }()

	var env apiResponse
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return fmt.Errorf("telegram %s: decode: %w", method, err)
	}
	if !env.OK {
		return fmt.Errorf("telegram %s: api error %d: %s", method, env.ErrorCode, env.Description)
	}
	if out != nil {
		if err := json.Unmarshal(env.Result, out); err != nil {
			return fmt.Errorf("telegram %s: unmarshal result: %w", method, err)
		}
	}
	return nil
}

// GetMe returns the bot's own account; useful as a startup token check.
func (c *Client) GetMe(ctx context.Context) (*User, error) {
	var u User
	if err := c.call(ctx, "getMe", struct{}{}, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

// SendMessage sends plain text to a chat.
func (c *Client) SendMessage(ctx context.Context, chatID int64, text string) error {
	payload := map[string]any{"chat_id": chatID, "text": text}
	return c.call(ctx, "sendMessage", payload, nil)
}

// SendChatAction shows a status hint ("typing") while the bot works on a reply.
func (c *Client) SendChatAction(ctx context.Context, chatID int64, action string) error {
	payload := map[string]any{"chat_id": chatID, "action": action}
	return c.call(ctx, "sendChatAction", payload, nil)
}

// SetWebhook registers url as the bot's webhook. Telegram will echo
// secretToken back in the X-Telegram-Bot-Api-Secret-Token header of every
// delivery, which the webhook handler verifies. Re-registering the same URL is
// idempotent.
func (c *Client) SetWebhook(ctx context.Context, url, secretToken string, allowedUpdates []string) error {
	payload := map[string]any{
		"url":             url,
		"secret_token":    secretToken,
		"allowed_updates": allowedUpdates,
	}
	return c.call(ctx, "setWebhook", payload, nil)
}

// DeleteWebhook unregisters the webhook (used when tearing down an instance).
func (c *Client) DeleteWebhook(ctx context.Context) error {
	return c.call(ctx, "deleteWebhook", struct{}{}, nil)
}

// GetWebhookInfo reports the current webhook registration.
func (c *Client) GetWebhookInfo(ctx context.Context) (*WebhookInfo, error) {
	var info WebhookInfo
	if err := c.call(ctx, "getWebhookInfo", struct{}{}, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

// GetFile resolves a file_id to a download path (photos, phase 2).
func (c *Client) GetFile(ctx context.Context, fileID string) (*File, error) {
	var f File
	if err := c.call(ctx, "getFile", map[string]any{"file_id": fileID}, &f); err != nil {
		return nil, err
	}
	return &f, nil
}
