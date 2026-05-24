package callbacks

import (
	"context"
	"strings"
)

type configDocumentSender interface {
	SendDocument(ctx context.Context, chatID int64, threadID *int64, filename string, data []byte, caption string) (int64, error)
}

func (r *Router) sendConfigDocument(ctx context.Context, chatID int64, threadID *int64, filename string, data []byte, caption string) error {
	sender, ok := r.tg.(configDocumentSender)
	if !ok {
		return nil
	}
	_, err := sender.SendDocument(ctx, chatID, threadID, filename, data, caption)
	return err
}

func safeConfigSlug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '_' || r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "config"
	}
	return out
}
