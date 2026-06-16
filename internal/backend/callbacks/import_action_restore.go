package callbacks

import "time"

func (a *ImportAction) restorePendingUpload(userID int64, up *pendingUpload) {
	if a.restoreFn != nil && up != nil && time.Now().Before(up.ExpiresAt) {
		a.restoreFn(userID, up)
	}
}
