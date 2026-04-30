package db

import (
	"fmt"
	"strconv"
)

// KV keys for operations-topic IDs (spec §7). Populated by
// `wg-monitor-cli set-topic --kind=summary|systemic --thread-id=N`.
const (
	KVKeySummaryTopicID  = "ui.summary_topic_id"
	KVKeySystemicTopicID = "ui.systemic_topic_id"
)

func topicKVKey(kind string) (string, error) {
	switch kind {
	case "summary":
		return KVKeySummaryTopicID, nil
	case "systemic":
		return KVKeySystemicTopicID, nil
	}
	return "", fmt.Errorf("invalid topic kind %q (want summary|systemic)", kind)
}

// SetTopicID upserts the topic id for kind ∈ {summary, systemic}.
func (r *KVRepo) SetTopicID(kind string, id int64) error {
	key, err := topicKVKey(kind)
	if err != nil {
		return err
	}
	return r.Set(key, strconv.FormatInt(id, 10))
}

// GetTopicID returns (id, true, nil) when set, (0, false, nil) when absent,
// or (0, false, err) when kind is invalid or the underlying store errored.
func (r *KVRepo) GetTopicID(kind string) (int64, bool, error) {
	key, err := topicKVKey(kind)
	if err != nil {
		return 0, false, err
	}
	raw, err := r.Get(key)
	if err != nil {
		return 0, false, err
	}
	if raw == "" {
		return 0, false, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, false, fmt.Errorf("kv: bad topic id %q: %w", raw, err)
	}
	return n, true, nil
}
