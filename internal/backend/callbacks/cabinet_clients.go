package callbacks

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Jkaotlic/wg-monitor/internal/backend"
	"github.com/Jkaotlic/wg-monitor/internal/backend/amnezia"
	"github.com/Jkaotlic/wg-monitor/internal/backend/hidemy"
)

// Клиенты кабинетов провайдеров. Жили в панелях бота; бот кабинеты
// теряет (цикл 3), а мини-апп и мастер замены ими пользуются.

func (r *Router) fetchAmneziaAccount(ctx context.Context, key string) (*amnezia.AccountInfo, error) {
	client := amnezia.New(r.cfg.AmneziaBaseURL)
	if err := client.Login(ctx, key); err != nil {
		return nil, err
	}
	return client.AccountInfo(ctx)
}

func (r *Router) revokeAmneziaCountryConfig(ctx context.Context, key, country string) error {
	client := amnezia.New(r.cfg.AmneziaBaseURL)
	if err := client.Login(ctx, key); err != nil {
		return err
	}
	return client.RevokeCountryConfig(ctx, country)
}

func amneziaIssuedCountrySet(info *amnezia.AccountInfo) map[string]bool {
	issued := map[string]bool{}
	if info == nil {
		return issued
	}
	for _, item := range info.IssuedConfigs {
		if item.SourceType == "country_config" {
			issued[strings.ToLower(item.CountryCode)] = true
		}
	}
	return issued
}

func (r *Router) hideMyServerByID(ctx context.Context, accessCode, serverID string) (hidemy.Server, error) {
	client := hidemy.New(r.cfg.HideMyBaseURL)
	servers, err := client.ServerList(ctx, accessCode)
	if err != nil {
		return hidemy.Server{}, err
	}
	server, ok := hidemy.FindServer(servers, serverID)
	if !ok {
		return hidemy.Server{}, fmt.Errorf("hidemy server not found")
	}
	return server, nil
}

func (r *Router) downloadHideMyConfig(ctx context.Context, accessCode, serverIP string) ([]byte, error) {
	client := hidemy.New(r.cfg.HideMyBaseURL)
	conf, err := client.DownloadAmneziaWG20Config(ctx, accessCode, serverIP)
	if err != nil {
		return nil, err
	}
	if !strings.Contains(string(conf), "[Interface]") || !strings.Contains(string(conf), "[Peer]") {
		return nil, errors.New("downloaded config does not look like WireGuard conf")
	}
	return conf, nil
}

// issueAmneziaConfig -- выпуск страны одним входом в кабинет: аккаунт,
// проверка слота, скачивание. Уже выпущенная страна слота не занимает и
// скачивается повторно.
func (r *Router) issueAmneziaConfig(ctx context.Context, key, country string) ([]byte, error) {
	client := amnezia.New(r.cfg.AmneziaBaseURL)
	if err := client.Login(ctx, key); err != nil {
		return nil, err
	}
	info, err := client.AccountInfo(ctx)
	if err != nil {
		return nil, err
	}
	if amneziaSlotBusy(info, country) {
		return nil, backend.ErrVPNSlotBusy
	}
	conf, err := client.DownloadConfig(ctx, country)
	if err != nil {
		return nil, err
	}
	if !strings.Contains(string(conf), "[Interface]") {
		return nil, errors.New("downloaded config does not look like WireGuard conf")
	}
	return conf, nil
}

// amneziaSlotBusy -- новая страна при полных слотах подписки. Кабинет без
// предела (max 0) слотов не считает.
func amneziaSlotBusy(info *amnezia.AccountInfo, country string) bool {
	if info == nil || info.MaxDeviceCount <= 0 {
		return false
	}
	if amneziaIssuedCountrySet(info)[strings.ToLower(strings.TrimSpace(country))] {
		return false
	}
	return info.ActiveDeviceCount >= info.MaxDeviceCount
}
