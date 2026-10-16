package callbacks

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Jkaotlic/wg-monitor/internal/backend"
	"github.com/Jkaotlic/wg-monitor/internal/backend/hidemy"
)

// Клиенты кабинетов провайдеров. Жили в панелях бота; бот кабинеты
// теряет (цикл 3), а мини-апп и мастер замены ими пользуются.

// Кабинет Amnezia Premium отключён целиком (v0.61.3, решение оператора
// 16.10.2026): автор awg-manager предупредил, что получение конфигов через
// API кабинета может заблокировать учётную запись. Клиента кабинета
// (пакет amnezia) отсюда больше не создаёт никто: выпуск, экран подписки,
// отзыв страны и проверка ключа отказывают backend.ErrAmneziaDisabled до
// любого сетевого обращения. Сам пакет оставлен до отдельного шага.

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
	if errors.Is(err, hidemy.ErrCodeRejected) {
		return nil, backend.ErrCabinetCodeRejected
	}
	if err != nil {
		return nil, err
	}
	if !strings.Contains(string(conf), "[Interface]") || !strings.Contains(string(conf), "[Peer]") {
		return nil, errors.New("downloaded config does not look like WireGuard conf")
	}
	return conf, nil
}
