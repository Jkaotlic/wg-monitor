package backend

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Jkaotlic/wg-monitor/internal/backend/awg3panel"
	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/linkrepair"
	"github.com/Jkaotlic/wg-monitor/internal/backend/replace"
)

// Провайдеры источника автопочинки. amnezia и hidemyname -- кабинеты
// (callbacks.Router), awg3 -- панель своего сервера (вариант «<панель>/<iface>»).
const (
	RepairProviderAmnezia = "amnezia"
	RepairProviderHideMy  = "hidemyname"
	RepairProviderAwg3    = "awg3"
)

type repairSource struct {
	cab    VPNCabinet
	panels Awg3Panels
	db     *db.DB
}

// RepairSource -- источник конфига для лесенки автопочинки. Провал кабинета
// или панели -- это не «попробуй ещё», а «нужен человек»: ключ не принят,
// код истёк, сервер не отвечает. Поэтому такие ошибки уходят движку как
// *linkrepair.NeedHuman с готовой фразой действия.
func RepairSource(cab VPNCabinet, panels Awg3Panels, database *db.DB) linkrepair.Source {
	return repairSource{cab: cab, panels: panels, db: database}
}

func (s repairSource) Issue(ctx context.Context, routerID int64, provider, option string) (replace.Issued, error) {
	if provider == RepairProviderAwg3 {
		return s.awg3(ctx, routerID, option, false)
	}
	return s.cabinet(ctx, routerID, provider, option)
}

func (s repairSource) Fresh(ctx context.Context, routerID int64, provider, option string) (replace.Issued, error) {
	if provider == RepairProviderAwg3 {
		return s.awg3(ctx, routerID, option, true)
	}
	// У кабинета «пересоздать» в том же варианте -- тот же выпуск: новый
	// конфиг той же страны кабинет отдаёт тем же вызовом.
	return s.cabinet(ctx, routerID, provider, option)
}

// Options -- варианты кабинета с подписью и отметкой «уже выпущен»: смена
// локации у «Amnezia Premium» берёт только ещё НЕ выпущенную страну (ключ
// выпущенной уже стоит в другом месте) и одну на настройку -- это место в
// подписке; человеку локация называется подписью кабинета.
func (s repairSource) Options(ctx context.Context, routerID int64, provider string) ([]linkrepair.Option, error) {
	if provider == RepairProviderAwg3 {
		return nil, nil
	}
	act, err := cabinetAction(provider)
	if err != nil {
		return nil, err
	}
	if s.cab == nil {
		return nil, &linkrepair.NeedHuman{Cause: errors.New("кабинеты провайдеров не подключены к бэкенду"), Action: act}
	}
	acc, err := s.cab.Account(ctx, routerID, provider)
	if err != nil {
		return nil, &linkrepair.NeedHuman{Cause: err, Action: act}
	}
	if !acc.Connected {
		return nil, &linkrepair.NeedHuman{Cause: errors.New("кабинет не подключён: " + acc.Note), Action: act}
	}
	out := make([]linkrepair.Option, 0, len(acc.Options))
	for _, o := range acc.Options {
		if id := strings.TrimSpace(o.ID); id != "" {
			out = append(out, linkrepair.Option{ID: id, Label: strings.TrimSpace(o.Label), Issued: o.Issued})
		}
	}
	return out, nil
}

// cabinetAction -- что сказать человеку, когда этот кабинет отказал.
func cabinetAction(provider string) (string, error) {
	switch provider {
	case RepairProviderAmnezia:
		return linkrepair.ActAmneziaKey, nil
	case RepairProviderHideMy:
		return linkrepair.ActHideMyCode, nil
	default:
		return "", &linkrepair.NeedHuman{Cause: fmt.Errorf("неизвестный источник %q", provider), Action: linkrepair.ActNoSource}
	}
}

func (s repairSource) cabinet(ctx context.Context, routerID int64, provider, option string) (replace.Issued, error) {
	act, err := cabinetAction(provider)
	if err != nil {
		return replace.Issued{}, err
	}
	if s.cab == nil {
		return replace.Issued{}, &linkrepair.NeedHuman{Cause: errors.New("кабинеты провайдеров не подключены к бэкенду"), Action: act}
	}
	// Ошибку кабинета отличить «ключ не принят» от «кабинет моргнул» по
	// тексту ненадёжно; любой отказ -- повод проверить ключ или код, и
	// человеку это сказано прямо.
	out, err := s.cab.IssueConfig(ctx, routerID, provider, option)
	if err != nil {
		return replace.Issued{}, &linkrepair.NeedHuman{Cause: err, Action: act}
	}
	if len(out.Conf) == 0 {
		return replace.Issued{}, errors.New("кабинет вернул пустой конфиг")
	}
	return replace.Issued{TunnelName: out.TunnelName, Conf: out.Conf, Backend: out.Backend}, nil
}

func (s repairSource) awg3(ctx context.Context, routerID int64, option string, fresh bool) (replace.Issued, error) {
	panel, iface, ok := strings.Cut(strings.TrimSpace(option), "/")
	panel, iface = strings.TrimSpace(panel), strings.TrimSpace(iface)
	if !ok || panel == "" || iface == "" {
		return replace.Issued{}, &linkrepair.NeedHuman{
			Cause:  fmt.Errorf("вариант своего сервера %q не в виде «панель/интерфейс»", option),
			Action: linkrepair.ActNoSource,
		}
	}
	if s.panels == nil {
		return replace.Issued{}, &linkrepair.NeedHuman{Cause: errors.New("панели своих серверов не подключены к бэкенду"), Action: linkrepair.ActVPSPanel(panel)}
	}
	u, err := s.db.Users().GetByID(routerID)
	if err != nil {
		return replace.Issued{}, fmt.Errorf("роутер не нашёлся: %w", err)
	}
	var rc awg3panel.RouterConfig
	if fresh {
		rc, err = s.panels.FreshConfigForRouter(ctx, panel, iface, u.Nickname)
	} else {
		rc, err = s.panels.ConfigForRouter(ctx, panel, iface, u.Nickname)
	}
	if err != nil {
		return replace.Issued{}, &linkrepair.NeedHuman{Cause: err, Action: linkrepair.ActVPSPanel(panel)}
	}
	if len(rc.Conf) == 0 {
		return replace.Issued{}, &linkrepair.NeedHuman{Cause: errors.New("панель вернула пустой конфиг"), Action: linkrepair.ActVPSPanel(panel)}
	}
	return replace.Issued{TunnelName: awg3panel.TunnelName(panel, iface), Conf: rc.Conf, Backend: "nativewg"}, nil
}
