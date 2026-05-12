package tg

import (
	"context"
	"encoding/json"
)

type Update struct {
	UpdateID      int64          `json:"update_id"`
	CallbackQuery *CallbackQuery `json:"callback_query,omitempty"`
	Message       *Message       `json:"message,omitempty"`
}

type CallbackQuery struct {
	ID      string  `json:"id"`
	From    User    `json:"from"`
	Message Message `json:"message"`
	Data    string  `json:"data"`
}

type Message struct {
	MessageID       int64     `json:"message_id"`
	Chat            Chat      `json:"chat"`
	From            User      `json:"from"`
	MessageThreadID *int64    `json:"message_thread_id,omitempty"`
	Text            string    `json:"text"`
	Document        *Document `json:"document,omitempty"`
	ForwardFrom     *User     `json:"forward_from,omitempty"`
}

// Document represents a TG file attachment on a Message.
type Document struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name"`
	FileSize int64  `json:"file_size"`
	MimeType string `json:"mime_type,omitempty"`
}

type User struct {
	ID int64 `json:"id"`
}

type Chat struct {
	ID int64 `json:"id"`
}

type getUpdatesReq struct {
	Offset         int64    `json:"offset,omitempty"`
	Timeout        int      `json:"timeout"`
	AllowedUpdates []string `json:"allowed_updates"`
}

// GetUpdates performs a long-poll request to the TG Bot API.
// Filters to callback_query AND message updates — the latter feeds
// ReplyKeyboard-button taps to the router (spec §6.2).
func (c *Client) GetUpdates(ctx context.Context, offset int64, timeoutSec int) ([]Update, error) {
	body, _ := json.Marshal(getUpdatesReq{
		Offset:         offset,
		Timeout:        timeoutSec,
		AllowedUpdates: []string{"callback_query", "message"},
	})
	var out []Update
	if err := c.callLongPoll(ctx, "getUpdates", body, &out); err != nil {
		return nil, err
	}
	return out, nil
}
