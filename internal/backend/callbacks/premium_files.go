package callbacks

import (
	"context"
	"errors"
	"strings"

	"github.com/Jkaotlic/wg-monitor/internal/backend"
	"github.com/Jkaotlic/wg-monitor/internal/backend/premiumfiles"
)

// OpenPremiumFiles is invoked explicitly by the owner from the cabinet screen.
// An empty key selects an existing secret. It never generates a country file.
func (r *Router) OpenPremiumFiles(ctx context.Context, routerID int64, key, keyID string) (premiumfiles.Account, string, error) {
	key = strings.TrimSpace(key)
	existing := key == ""
	if existing {
		var err error
		key, err = r.getAmneziaKeyByID(routerID, keyID)
		if err != nil {
			return premiumfiles.Account{}, "", err
		}
		if key == "" {
			return premiumfiles.Account{}, "", backend.ErrCabinetSecretNotFound
		}
	}
	c := premiumfiles.New(r.cfg.AmneziaFilesBaseURL)
	if err := c.Login(ctx, key); err != nil {
		return premiumfiles.Account{}, "", err
	}
	account, err := c.Account(ctx)
	if err != nil {
		return premiumfiles.Account{}, "", err
	}
	if !existing {
		if _, err := r.addAmneziaKeyLabeled(routerID, key, ""); err != nil {
			return premiumfiles.Account{}, "", err
		}
	}
	return account, amneziaKeyID(key), nil
}

func (r *Router) DownloadPremiumFile(ctx context.Context, routerID int64, keyID, country, declared string) ([]byte, error) {
	// Serialize issue requests. No automatic retries: a timed-out issuance can
	// already have consumed a slot at the provider.
	if !r.premiumFilesMu.TryLock() {
		return nil, premiumfiles.ErrBusy
	}
	defer r.premiumFilesMu.Unlock()
	key, err := r.getAmneziaKeyByID(routerID, keyID)
	if err != nil {
		return nil, err
	}
	if key == "" {
		return nil, backend.ErrCabinetSecretNotFound
	}
	c := premiumfiles.New(r.cfg.AmneziaFilesBaseURL)
	if err := c.Login(ctx, key); err != nil {
		return nil, err
	}
	account, err := c.Account(ctx)
	if err != nil {
		return nil, err
	}
	found := false
	for _, item := range account.Countries {
		if strings.EqualFold(item.Code, country) {
			found = true
			break
		}
	}
	if !found {
		return nil, errors.New("страна отсутствует в подписке")
	}
	return c.Download(ctx, country, declared)
}
