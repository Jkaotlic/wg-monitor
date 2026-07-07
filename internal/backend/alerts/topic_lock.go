package alerts

import "sync"

// topicLocks backs LockTopicCreation: one *sync.Mutex per userID, created on
// demand and never evicted. Keys are cheap (int64 user IDs, one per
// registered router) so this mirrors the never-cleaned, process-lifetime
// bookkeeping already used elsewhere in this package.
var topicLocks = struct {
	mu   sync.Mutex
	byID map[int64]*sync.Mutex
}{byID: make(map[int64]*sync.Mutex)}

// LockTopicCreation serializes concurrent topic-creation attempts for the
// same user across every call site in the process: Dispatcher.ensureTopic
// (alert-driven HARD/Recovery path) and the admin slash-commands
// /ensure_topics and /recreate_topic (callbacks.adminEnsureTopics /
// adminRecreateTopic), which call EnsureTopicForUser directly (C5).
//
// EnsureTopicForUser documents that it is NOT goroutine-safe: two concurrent
// calls for the same user can both observe "no thread yet", both call
// CreateForumTopic, and whichever UpdateTelegramTopic write lands last wins
// — leaving an orphaned TG topic that no user record points at. Every call
// site that may invoke EnsureTopicForUser concurrently for the same user
// MUST hold this lock around the ENTIRE call (read-check-create-persist),
// so the loser of the race sees the winner's already-persisted thread id and
// returns it instead of creating a duplicate.
//
// Callers must invoke the returned unlock exactly once, typically via
// defer:
//
//	unlock := alerts.LockTopicCreation(userID)
//	defer unlock()
//	ref, err := alerts.EnsureTopicForUser(ctx, tg, db, chatID, userID, force)
func LockTopicCreation(userID int64) (unlock func()) {
	topicLocks.mu.Lock()
	l, ok := topicLocks.byID[userID]
	if !ok {
		l = &sync.Mutex{}
		topicLocks.byID[userID] = l
	}
	topicLocks.mu.Unlock()

	l.Lock()
	return l.Unlock
}
