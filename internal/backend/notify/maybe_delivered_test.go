package notify

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
)

func TestMaybeDelivered(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"срок контекста у получателя", fmt.Errorf("%w: %w", ErrNoneDelivered, context.DeadlineExceeded), true},
		{"заведомый отказ", fmt.Errorf("%w: %w", ErrNoneDelivered, tg.NotSent(errors.New("refused"))), false},
		{"ошибка базы без получателей", context.DeadlineExceeded, false},
	}
	for _, c := range cases {
		if got := MaybeDelivered(c.err); got != c.want {
			t.Errorf("%s: MaybeDelivered = %v, want %v", c.name, got, c.want)
		}
	}
}
