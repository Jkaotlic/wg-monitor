package db

import (
	"fmt"
	"time"
)

func mobileSleepKVKey(userID int64) string {
	return fmt.Sprintf("mobile.sleep_notified.%d", userID)
}

func mobileWakeKVKey(userID int64) string {
	return fmt.Sprintf("mobile.wake_notified.%d", userID)
}

// GetMobileSleepNotifiedAt returns the last heartbeat timestamp for which a
// mobile sleep info-card has already been sent. Empty key means no persisted
// notification for the current sleep cycle.
func (r *KVRepo) GetMobileSleepNotifiedAt(userID int64) (time.Time, bool, error) {
	raw, err := r.Get(mobileSleepKVKey(userID))
	if err != nil {
		return time.Time{}, false, err
	}
	if raw == "" {
		return time.Time{}, false, nil
	}
	t, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("mobile sleep notified: bad timestamp %q: %w", raw, err)
	}
	return t.UTC(), true, nil
}

// SetMobileSleepNotifiedAt records the last heartbeat timestamp that produced
// a sleep info-card, making the one-shot guard survive backend restarts.
func (r *KVRepo) SetMobileSleepNotifiedAt(userID int64, lastSeen time.Time) error {
	return r.Set(mobileSleepKVKey(userID), lastSeen.UTC().Format(time.RFC3339Nano))
}

// ClearMobileSleepNotified clears the persisted one-shot guard when a fresh
// heartbeat arrives, so the next sleep cycle can notify once again.
func (r *KVRepo) ClearMobileSleepNotified(userID int64) error {
	return r.Set(mobileSleepKVKey(userID), "")
}

func (r *KVRepo) GetMobileWakeNotifiedAt(userID int64) (time.Time, bool, error) {
	raw, err := r.Get(mobileWakeKVKey(userID))
	if err != nil {
		return time.Time{}, false, err
	}
	if raw == "" {
		return time.Time{}, false, nil
	}
	t, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("mobile wake notified: bad timestamp %q: %w", raw, err)
	}
	return t.UTC(), true, nil
}

func (r *KVRepo) SetMobileWakeNotifiedAt(userID int64, t time.Time) error {
	return r.Set(mobileWakeKVKey(userID), t.UTC().Format(time.RFC3339Nano))
}
