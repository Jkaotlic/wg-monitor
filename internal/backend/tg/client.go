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
	"fmt"
	"io"
	"net/http"
)

const DefaultBaseURL = "https://api.telegram.org/bot"

type Client struct {
	BaseURL string // typically https://api.telegram.org/bot — Token is appended
	Token   string
	HTTP    *http.Client
	// LongPollHTTP is used for getUpdates — its Timeout must exceed the
	// server-side hold (typically 30s) plus network grace. Falls back to HTTP
	// when nil, but production callers should always set a separate client
	// (HTTP.Timeout=15s would otherwise rip every long-poll mid-flight).
	LongPollHTTP *http.Client
}

type apiResp struct {
	OK          bool            `json:"ok"`
	Description string          `json:"description"`
	ErrorCode   int             `json:"error_code"`
	Result      json.RawMessage `json:"result"`
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
		return fmt.Errorf("tg %s: %w", method, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	var ar apiResp
	if err := json.Unmarshal(raw, &ar); err != nil {
		return fmt.Errorf("tg %s: bad response (status %d): %s", method, resp.StatusCode, string(raw))
	}
	if !ar.OK {
		return fmt.Errorf("tg %s: %s (code=%d)", method, ar.Description, ar.ErrorCode)
	}
	if dst != nil {
		if err := json.Unmarshal(ar.Result, dst); err != nil {
			return fmt.Errorf("tg %s: decode result: %w", method, err)
		}
	}
	return nil
}
