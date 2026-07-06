// internal/backend/tg/client.go
// Package tg is a hand-rolled net/http client for the small slice of the
// Telegram Bot API we need in Stage 1 (sendMessage, createForumTopic).
// We picked this over go-telegram-bot-api/v5 to keep the Stage 1 dep tree
// lean — full library will earn its place when callbacks land in Stage 2.
package tg

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// APIError is the structured form of a non-OK Telegram Bot API response.
// Callers should use errors.As to extract it and inspect Code/Description
// for self-heal decisions (see IsTopicNotFound).
type APIError struct {
	Method      string
	Description string
	Code        int
	RetryAfter  time.Duration
}

func (e *APIError) Error() string {
	return fmt.Sprintf("tg %s: %s (code=%d)", e.Method, e.Description, e.Code)
}

// IsTopicNotFound reports whether err signals the target forum topic no
// longer exists in TG. Used by the alert dispatcher to clear the cached
// telegram_thread_id and recreate the topic on the next attempt rather
// than retry forever against a dead id.
func IsTopicNotFound(err error) bool {
	var ae *APIError
	if !errors.As(err, &ae) {
		return false
	}
	if ae.Code != 400 {
		return false
	}
	d := strings.ToLower(ae.Description)
	// topic_closed deliberately NOT included: a closed topic still exists
	// and can be reopened by an admin; recreating would orphan history.
	return strings.Contains(d, "message thread not found") ||
		strings.Contains(d, "message_thread_not_found") ||
		strings.Contains(d, "topic_deleted")
}

// RateLimitDelay reports Telegram flood-control / Too Many Requests errors.
// The Bot API may include parameters.retry_after in seconds; when absent, a
// zero delay still tells callers to stop the current send batch.
func RateLimitDelay(err error) (time.Duration, bool) {
	var ae *APIError
	if !errors.As(err, &ae) {
		return 0, false
	}
	if ae.Code != http.StatusTooManyRequests {
		return 0, false
	}
	return ae.RetryAfter, true
}

const (
	DefaultBaseURL       = "https://api.telegram.org/bot"
	maxDownloadFileBytes = 20 * 1024 * 1024
)

type Client struct {
	BaseURL string // typically https://api.telegram.org/bot — Token is appended
	Token   string
	HTTP    *http.Client
	// LongPollHTTP is used for getUpdates — its Timeout must exceed the
	// server-side hold (typically 30s) plus network grace. Falls back to HTTP
	// when nil, but production callers should always set a separate client
	// (HTTP.Timeout=15s would otherwise rip every long-poll mid-flight).
	LongPollHTTP *http.Client
	// Logger surfaces TG API errors at Warn level (4xx/5xx + transport).
	// Optional — nil disables logging entirely (preserves test silence).
	// CRITICAL: errors emitted from this client redact the URL token before
	// wrapping, so logs never expose the bot-token even when slog dumps
	// `err`.
	Logger *slog.Logger
}

type apiResp struct {
	OK          bool                `json:"ok"`
	Description string              `json:"description"`
	ErrorCode   int                 `json:"error_code"`
	Parameters  *responseParameters `json:"parameters,omitempty"`
	Result      json.RawMessage     `json:"result"`
}

type responseParameters struct {
	RetryAfter int `json:"retry_after,omitempty"`
}

func (r apiResp) retryAfter() time.Duration {
	if r.Parameters == nil || r.Parameters.RetryAfter <= 0 {
		return 0
	}
	return time.Duration(r.Parameters.RetryAfter) * time.Second
}

type sendMessageReq struct {
	ChatID           int64  `json:"chat_id"`
	MessageThreadID  *int64 `json:"message_thread_id,omitempty"`
	Text             string `json:"text"`
	ParseMode        string `json:"parse_mode,omitempty"`
	ReplyToMessageID *int64 `json:"reply_to_message_id,omitempty"`
}

type sendMessageResult struct {
	MessageID int64 `json:"message_id"`
}

// SendMessage returns the message_id of the new message.
// Pass nil for threadID to post in General; pass nil for replyTo for top-level msgs.
func (c *Client) SendMessage(ctx context.Context, chatID int64, threadID *int64, text, parseMode string, replyTo *int64) (int64, error) {
	body, _ := json.Marshal(sendMessageReq{
		ChatID:           chatID,
		MessageThreadID:  threadID,
		Text:             text,
		ParseMode:        parseMode,
		ReplyToMessageID: replyTo,
	})
	var out sendMessageResult
	if err := c.call(ctx, "sendMessage", body, &out); err != nil {
		return 0, err
	}
	return out.MessageID, nil
}

type createTopicReq struct {
	ChatID    int64  `json:"chat_id"`
	Name      string `json:"name"`
	IconColor int    `json:"icon_color,omitempty"`
}

type createTopicResult struct {
	MessageThreadID int64 `json:"message_thread_id"`
}

func (c *Client) CreateForumTopic(ctx context.Context, chatID int64, name string, iconColor int) (int64, error) {
	body, _ := json.Marshal(createTopicReq{ChatID: chatID, Name: name, IconColor: iconColor})
	var out createTopicResult
	if err := c.call(ctx, "createForumTopic", body, &out); err != nil {
		return 0, err
	}
	return out.MessageThreadID, nil
}

type sendMessageWithKBReq struct {
	ChatID           int64                 `json:"chat_id"`
	MessageThreadID  *int64                `json:"message_thread_id,omitempty"`
	Text             string                `json:"text"`
	ParseMode        string                `json:"parse_mode,omitempty"`
	ReplyToMessageID *int64                `json:"reply_to_message_id,omitempty"`
	ReplyMarkup      *InlineKeyboardMarkup `json:"reply_markup,omitempty"`
}

// SendMessageWithKeyboard sends a message with an attached inline keyboard.
// markup must be non-nil; for plain messages use SendMessage.
func (c *Client) SendMessageWithKeyboard(ctx context.Context, chatID int64, threadID *int64, text, parseMode string, replyTo *int64, markup *InlineKeyboardMarkup) (int64, error) {
	body, _ := json.Marshal(sendMessageWithKBReq{
		ChatID:           chatID,
		MessageThreadID:  threadID,
		Text:             text,
		ParseMode:        parseMode,
		ReplyToMessageID: replyTo,
		ReplyMarkup:      markup,
	})
	var out sendMessageResult
	if err := c.call(ctx, "sendMessage", body, &out); err != nil {
		return 0, err
	}
	return out.MessageID, nil
}

type answerCBReq struct {
	CallbackQueryID string `json:"callback_query_id"`
	Text            string `json:"text,omitempty"`
}

// AnswerCallbackQuery closes the loading spinner on the user's button.
// text (optional, ≤200 chars) shows as a transient toast; pass "" for silent close.
func (c *Client) AnswerCallbackQuery(ctx context.Context, callbackID, text string) error {
	body, _ := json.Marshal(answerCBReq{CallbackQueryID: callbackID, Text: text})
	return c.call(ctx, "answerCallbackQuery", body, nil)
}

type editMessageReq struct {
	ChatID      int64                 `json:"chat_id"`
	MessageID   int64                 `json:"message_id"`
	Text        string                `json:"text"`
	ParseMode   string                `json:"parse_mode,omitempty"`
	ReplyMarkup *InlineKeyboardMarkup `json:"reply_markup,omitempty"`
}

// EditMessageText edits an existing message. markup contract:
//
//	nil  → reply_markup not sent (TG does not change existing keyboard)
//	&{}  → reply_markup sent with empty inline_keyboard array (removes buttons)
func (c *Client) EditMessageText(ctx context.Context, chatID, messageID int64, text, parseMode string, markup *InlineKeyboardMarkup) error {
	body, _ := json.Marshal(editMessageReq{
		ChatID:      chatID,
		MessageID:   messageID,
		Text:        text,
		ParseMode:   parseMode,
		ReplyMarkup: markup,
	})
	return c.call(ctx, "editMessageText", body, nil)
}

// sendMessageWithRMReq lets reply_markup be ANY of {InlineKeyboardMarkup,
// ReplyKeyboardMarkup, ReplyKeyboardRemove}. Encoded as raw json.RawMessage
// avoids polymorphic struct fields and keeps the wire format clean.
type sendMessageWithRMReq struct {
	ChatID           int64           `json:"chat_id"`
	MessageThreadID  *int64          `json:"message_thread_id,omitempty"`
	Text             string          `json:"text"`
	ParseMode        string          `json:"parse_mode,omitempty"`
	ReplyToMessageID *int64          `json:"reply_to_message_id,omitempty"`
	ReplyMarkup      json.RawMessage `json:"reply_markup,omitempty"`
}

// SendMessageWithReplyKeyboard sends a message with any kind of reply_markup
// payload. markup may be:
//   - *InlineKeyboardMarkup
//   - *ReplyKeyboardMarkup
//   - *ReplyKeyboardRemove
//   - nil (no reply_markup field)
//
// We marshal first, then forward as RawMessage so json.Marshal produces the
// canonical TG payload regardless of which variant was passed.
func (c *Client) SendMessageWithReplyKeyboard(ctx context.Context, chatID int64, threadID *int64, text, parseMode string, replyTo *int64, markup any) (int64, error) {
	var rawMarkup json.RawMessage
	if markup != nil {
		raw, err := json.Marshal(markup)
		if err != nil {
			return 0, fmt.Errorf("tg sendMessage: marshal reply_markup: %w", err)
		}
		rawMarkup = raw
	}
	body, _ := json.Marshal(sendMessageWithRMReq{
		ChatID:           chatID,
		MessageThreadID:  threadID,
		Text:             text,
		ParseMode:        parseMode,
		ReplyToMessageID: replyTo,
		ReplyMarkup:      rawMarkup,
	})
	var out sendMessageResult
	if err := c.call(ctx, "sendMessage", body, &out); err != nil {
		return 0, err
	}
	return out.MessageID, nil
}

// SendDocument uploads a small in-memory document to a chat/topic.
func (c *Client) SendDocument(ctx context.Context, chatID int64, threadID *int64, filename string, data []byte, caption string) (int64, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("chat_id", strconv.FormatInt(chatID, 10))
	if threadID != nil {
		_ = mw.WriteField("message_thread_id", strconv.FormatInt(*threadID, 10))
	}
	if caption != "" {
		_ = mw.WriteField("caption", caption)
	}
	part, err := mw.CreateFormFile("document", filename)
	if err != nil {
		return 0, fmt.Errorf("tg sendDocument: create form file: %w", err)
	}
	if _, err := part.Write(data); err != nil {
		return 0, fmt.Errorf("tg sendDocument: write file: %w", err)
	}
	if err := mw.Close(); err != nil {
		return 0, fmt.Errorf("tg sendDocument: close multipart: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+c.Token+"/sendDocument", &body)
	if err != nil {
		return 0, fmt.Errorf("tg sendDocument: build request: %w", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := c.HTTP.Do(req)
	if err != nil {
		safe := redactURLError(err)
		c.warn("sendDocument", "transport error", "err", safe)
		return 0, fmt.Errorf("tg sendDocument: %s", safe)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	var ar apiResp
	if err := json.Unmarshal(raw, &ar); err != nil {
		c.warn("sendDocument", "bad response", "status", resp.StatusCode, "body_len", len(raw))
		return 0, fmt.Errorf("tg sendDocument: bad response (status %d): %s", resp.StatusCode, string(raw))
	}
	if !ar.OK {
		c.warn("sendDocument", "api error", "tg_code", ar.ErrorCode, "tg_description", ar.Description)
		return 0, &APIError{Method: "sendDocument", Description: ar.Description, Code: ar.ErrorCode, RetryAfter: ar.retryAfter()}
	}
	var out sendMessageResult
	if err := json.Unmarshal(ar.Result, &out); err != nil {
		return 0, fmt.Errorf("tg sendDocument: decode result: %w", err)
	}
	return out.MessageID, nil
}

type deleteMessageReq struct {
	ChatID    int64 `json:"chat_id"`
	MessageID int64 `json:"message_id"`
}

// DeleteMessage calls TG `deleteMessage`. Used to wipe the operator's
// "📊 Что происходит?" message after the smart-reply is composed (spec §6.2 b).
// Caller is responsible for swallowing 403 / can_delete_messages errors.
func (c *Client) DeleteMessage(ctx context.Context, chatID, messageID int64) error {
	body, _ := json.Marshal(deleteMessageReq{ChatID: chatID, MessageID: messageID})
	return c.call(ctx, "deleteMessage", body, nil)
}

type getFileReq struct {
	FileID string `json:"file_id"`
}

type getFileResult struct {
	FilePath string `json:"file_path"`
}

// GetFile returns the server-side file_path for a TG file_id. The path is
// valid for 1 hour. Use DownloadFile to fetch the actual bytes.
func (c *Client) GetFile(ctx context.Context, fileID string) (string, error) {
	body, _ := json.Marshal(getFileReq{FileID: fileID})
	var out getFileResult
	if err := c.call(ctx, "getFile", body, &out); err != nil {
		return "", err
	}
	return out.FilePath, nil
}

// DownloadFile fetches raw bytes from the TG file CDN.
// filePath comes from GetFile. Limit 20 MB (TG Bot API hard cap).
//
// CRITICAL: both the build-request and transport-Do failures below can
// surface a *url.Error whose Error() embeds the full request URL —
// including our bot-token. (The file-CDN URL shape is
// ".../file/bot<token>/<filePath>", distinct from the ".../bot<token>/<method>"
// shape callWith uses, which is why redactURLError recognises both prefixes.)
// Both paths are redacted before being returned: callers — notably the
// document-upload chat handler — relay these errors verbatim into Telegram
// chat text, so an unredacted error here would leak the live bot-token to
// every member of the topic.
func (c *Client) DownloadFile(ctx context.Context, filePath string) ([]byte, error) {
	fileBase := strings.TrimSuffix(c.BaseURL, "bot") + "file/bot"
	url := fileBase + c.Token + "/" + filePath
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		safe := redactURLError(err)
		c.warn("DownloadFile", "build request failed", "err", safe)
		return nil, fmt.Errorf("tg DownloadFile: build request: %s", safe)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		safe := redactURLError(err)
		c.warn("DownloadFile", "transport error", "err", safe)
		return nil, fmt.Errorf("tg DownloadFile: %s", safe)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("tg DownloadFile: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxDownloadFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("tg DownloadFile: read response: %w", err)
	}
	if len(data) > maxDownloadFileBytes {
		return nil, fmt.Errorf("tg DownloadFile: file exceeds %d bytes", maxDownloadFileBytes)
	}
	return data, nil
}

// BotCommand mirrors the TG Bot API BotCommand object used by setMyCommands.
type BotCommand struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

// BotCommandScope mirrors the TG Bot API BotCommandScope union for the scope
// types this bot needs: default, chat, and chat_member.
type BotCommandScope struct {
	Type   string `json:"type"`
	ChatID int64  `json:"chat_id,omitempty"`
	UserID int64  `json:"user_id,omitempty"`
}

type setMyCommandsReq struct {
	Commands []BotCommand     `json:"commands"`
	Scope    *BotCommandScope `json:"scope,omitempty"`
}

type setChatMenuButtonReq struct {
	MenuButton menuButton `json:"menu_button"`
}

type menuButton struct {
	Type string `json:"type"`
}

// SetMyCommands registers the bot's slash-command menu so TG clients show
// the commands in the command picker. Idempotent — TG replaces the previous
// list each call. Errors are non-fatal at call sites (the bot keeps working
// without the menu hint).
func (c *Client) SetMyCommands(ctx context.Context, cmds []BotCommand) error {
	body, _ := json.Marshal(setMyCommandsReq{Commands: cmds})
	return c.call(ctx, "setMyCommands", body, nil)
}

func (c *Client) SetMyCommandsWithScope(ctx context.Context, cmds []BotCommand, scope BotCommandScope) error {
	body, _ := json.Marshal(setMyCommandsReq{Commands: cmds, Scope: &scope})
	return c.call(ctx, "setMyCommands", body, nil)
}

func (c *Client) SetCommandsMenuButton(ctx context.Context) error {
	body, _ := json.Marshal(setChatMenuButtonReq{MenuButton: menuButton{Type: "commands"}})
	return c.call(ctx, "setChatMenuButton", body, nil)
}

func (c *Client) call(ctx context.Context, method string, body []byte, dst any) error {
	return c.callWith(ctx, c.HTTP, method, body, dst)
}

func (c *Client) callLongPoll(ctx context.Context, method string, body []byte, dst any) error {
	httpc := c.LongPollHTTP
	if httpc == nil {
		httpc = c.HTTP
	}
	return c.callWith(ctx, httpc, method, body, dst)
}

func (c *Client) callWith(ctx context.Context, httpc *http.Client, method string, body []byte, dst any) error {
	url := c.BaseURL + c.Token + "/" + method
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("tg %s: build request: %w", method, err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpc.Do(req)
	if err != nil {
		// Go's *url.Error embeds the full URL — including our bot-token —
		// in Error(), and `%w`-wrapping carries that all the way to slog
		// JSON. Strip it down to the method name; details (timeout / DNS
		// failure / etc.) survive on the underlying error.
		safe := redactURLError(err)
		c.warn(method, "transport error", "err", safe)
		return fmt.Errorf("tg %s: %s", method, safe)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	var ar apiResp
	if err := json.Unmarshal(raw, &ar); err != nil {
		c.warn(method, "bad response", "status", resp.StatusCode, "body_len", len(raw))
		return fmt.Errorf("tg %s: bad response (status %d): %s", method, resp.StatusCode, string(raw))
	}
	if !ar.OK {
		if isMessageNotModified(method, ar.ErrorCode, ar.Description) {
			return nil
		}
		c.warn(method, "api error", "tg_code", ar.ErrorCode, "tg_description", ar.Description)
		return &APIError{Method: method, Description: ar.Description, Code: ar.ErrorCode, RetryAfter: ar.retryAfter()}
	}
	if dst != nil {
		if err := json.Unmarshal(ar.Result, dst); err != nil {
			c.warn(method, "decode result failed", "err", err)
			return fmt.Errorf("tg %s: decode result: %w", method, err)
		}
	}
	return nil
}

// warn emits a structured log if Logger is configured. method is the TG
// API method name (sendMessage, getUpdates, ...). attrs follow slog's
// key-value convention.
func (c *Client) warn(method, msg string, attrs ...any) {
	if c.Logger == nil {
		return
	}
	c.Logger.Warn("tg "+method+": "+msg, attrs...)
}

func isMessageNotModified(method string, code int, description string) bool {
	if method != "editMessageText" || code != 400 {
		return false
	}
	return strings.Contains(strings.ToLower(description), "message is not modified")
}

// redactURLError replaces the embedded URL in `*url.Error.Error()` with one
// where the bot-token segment is masked as `***`. Best-effort: any error
// shape we don't recognise is returned unchanged. Anchored by two literal
// prefixes:
//   - "https://api.telegram.org/bot" — method calls (DefaultBaseURL), used
//     by callWith/SendDocument.
//   - "https://api.telegram.org/file/bot" — file-CDN downloads (DownloadFile),
//     which insert a "file/" path segment ahead of "bot" and so do NOT match
//     the method-call prefix above; it needs its own anchor.
//
// The two prefixes are mutually exclusive on real inputs, so checking the
// longer one first is sufficient — no input matches both.
func redactURLError(err error) error {
	if err == nil {
		return nil
	}
	s := err.Error()
	for _, prefix := range [...]string{
		"https://api.telegram.org/file/bot",
		"https://api.telegram.org/bot",
	} {
		idx := strings.Index(s, prefix)
		if idx < 0 {
			continue
		}
		// Token = everything between the prefix and the next "/" (path separator).
		tail := s[idx+len(prefix):]
		slash := strings.Index(tail, "/")
		if slash <= 0 {
			continue
		}
		return errSanitized(s[:idx+len(prefix)] + "***" + tail[slash:])
	}
	return err
}

type errSanitized string

func (e errSanitized) Error() string { return string(e) }
