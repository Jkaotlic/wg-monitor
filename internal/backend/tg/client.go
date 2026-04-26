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

func (c *Client) call(ctx context.Context, method string, body []byte, dst any) error {
	url := c.BaseURL + c.Token + "/" + method
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
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
		return json.Unmarshal(ar.Result, dst)
	}
	return nil
}
