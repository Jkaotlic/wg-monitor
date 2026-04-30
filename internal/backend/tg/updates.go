package tg

import (
	"context"
	"encoding/json"
)

type Update struct {
	UpdateID      int64          `json:"update_id"`
	CallbackQuery *CallbackQuery `json:"callback_query,omitempty"`
}

type CallbackQuery struct {
	ID      string  `json:"id"`
	From    User    `json:"from"`
	Message Message `json:"message"`
	Data    string  `json:"data"`
}

type Message struct {
	MessageID       int64  `json:"message_id"`
	Chat            Chat   `json:"chat"`
	MessageThreadID *int64 `json:"message_thread_id,omitempty"`
	Text            string `json:"text"`
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
// timeoutSec is the server-side hold time (TG keeps connection open for this many seconds
// if no updates are available). offset = last_processed_update_id + 1.
// Filters to callback_query updates only.
func (c *Client) GetUpdates(ctx context.Context, offset int64, timeoutSec int) ([]Update, error) {
	body, _ := json.Marshal(getUpdatesReq{
		Offset:         offset,
		Timeout:        timeoutSec,
		AllowedUpdates: []string{"callback_query"},
	})
	var out []Update
	if err := c.callLongPoll(ctx, "getUpdates", body, &out); err != nil {
		return nil, err
	}
	return out, nil
}
