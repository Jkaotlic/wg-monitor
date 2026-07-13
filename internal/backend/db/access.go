package db

import (
	"errors"
)

// RouterAccessRole reports how telegramUserID relates to the router
// identified by routerUserID: "owner", "operator", or "" if neither. This is
// the topic-independent core of callbacks.Router.aclAllow's owner/operator
// check, extracted so the Telegram bot and the mini app resolve access the
// same way and cannot drift apart. Admin bypass stays a caller concern —
// this package has no notion of "admin" (that's a Config/deployment concept,
// not a persisted one).
func (d *DB) RouterAccessRole(routerUserID, telegramUserID int64) (string, error) {
	user, err := d.Users().GetByID(routerUserID)
	if err != nil {
		// Treat "router not found" as empty role, not an error.
		if errors.Is(err, ErrUserNotFound) {
			return "", nil
		}
		return "", err
	}
	if user == nil {
		return "", nil
	}
	if user.TelegramUserID != nil && *user.TelegramUserID == telegramUserID {
		return "owner", nil
	}
	if d.RouterOperators().HasAccess(routerUserID, telegramUserID) {
		return "operator", nil
	}
	return "", nil
}

// AccessibleRouterIDs returns the IDs of routers telegramUserID owns or
// operates. It scans the full router list — fine at this project's fleet
// scale (single-operator, tens of routers); a joined query is a
// straightforward follow-up if that ever changes.
func (d *DB) AccessibleRouterIDs(telegramUserID int64) ([]int64, error) {
	all, err := d.Users().GetAll()
	if err != nil {
		return nil, err
	}
	var ids []int64
	for _, u := range all {
		if u.TelegramUserID != nil && *u.TelegramUserID == telegramUserID {
			ids = append(ids, u.ID)
			continue
		}
		if d.RouterOperators().HasAccess(u.ID, telegramUserID) {
			ids = append(ids, u.ID)
		}
	}
	return ids, nil
}
