package backend

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/linkrepair"
	"github.com/Jkaotlic/wg-monitor/internal/backend/replace"
)

// Переходники между контрактами мини-аппа и движка замены. Движок не знает
// про пакет backend (иначе получился бы цикл), поэтому одинаковые по смыслу
// типы переводятся здесь -- в одном месте и явно.

type replaceCabinet struct{ inner VPNCabinet }

// ReplaceCabinet превращает кабинет мини-аппа в кабинет движка. Содержимое
// конфига как не показывалось клиенту, так и не показывается: оно едет из
// кабинета прямо в команду агенту.
func ReplaceCabinet(c VPNCabinet) replace.Cabinet { return replaceCabinet{inner: c} }

func (c replaceCabinet) IssueConfig(ctx context.Context, routerID int64, provider, optionID string) (replace.Issued, error) {
	out, err := c.inner.IssueConfig(ctx, routerID, provider, optionID)
	if err != nil {
		return replace.Issued{}, err
	}
	return replace.Issued{TunnelName: out.TunnelName, Conf: out.Conf, Backend: out.Backend}, nil
}

type replaceOrigin struct{ db *db.DB }

// ReplaceOrigin записывает происхождение конфига в базу бэкенда.
func ReplaceOrigin(database *db.DB) replace.OriginWriter { return replaceOrigin{db: database} }

func (o replaceOrigin) Record(routerID int64, tunnelID, tunnelName, provider, option string, issuedAt time.Time) error {
	return o.db.TunnelOrigins().Record(routerID, tunnelID, tunnelName, provider, option, issuedAt, 0)
}

// RepairOriginReader -- чем был поднят VPN-туннель (tunnel_config_origin).
// Движок починки его больше не читает (источник -- настройка автопочинки
// туннеля); остаётся для подсказки источника при включении.
type RepairOriginReader interface {
	Get(routerID int64, tunnelID string) (provider, option string, ok bool)
}

type linkRepairOrigin struct{ db *db.DB }

// LinkRepairOrigin читает происхождение конфига. Отсутствие строки -- это
// «система не помнит», а не ошибка: у линий, заведённых руками или до мастера
// замены, происхождения нет, и выдумать его нечем.
func LinkRepairOrigin(database *db.DB) RepairOriginReader { return linkRepairOrigin{db: database} }

func (o linkRepairOrigin) Get(routerID int64, tunnelID string) (string, string, bool) {
	got, ok, err := o.db.TunnelOrigins().Get(routerID, tunnelID)
	if err != nil || !ok {
		return "", "", false
	}
	return got.Provider, got.Variant, true
}

// AutoRepairHint -- для диспетчера тревог: включена ли автопочинка у
// VPN-туннеля этой проверки. Тогда тревога последней строкой обещает дописать
// ход починки сюда же. Не VPN-туннель или настройка не прочиталась -- нет:
// пообещать и не прийти хуже, чем промолчать.
func AutoRepairHint(database *db.DB) func(userID int64, checkName string) bool {
	return func(userID int64, checkName string) bool {
		tid, ok := strings.CutPrefix(checkName, "tunnel_")
		if !ok || tid == "" {
			return false
		}
		s, found, err := database.TunnelRepairSettings().Get(userID, tid)
		return err == nil && found && s.Enabled
	}
}

// LinkRepairSettings -- настройка автопочинки туннеля глазами движка. Ошибка
// чтения -- «выключено»: починка без явного согласия хуже, чем её отсутствие.
func LinkRepairSettings(database *db.DB, logger *slog.Logger) func(routerID int64, tunnelID string) (linkrepair.Setting, bool) {
	return func(routerID int64, tunnelID string) (linkrepair.Setting, bool) {
		s, ok, err := database.TunnelRepairSettings().Get(routerID, tunnelID)
		if err != nil {
			if logger != nil {
				logger.Warn("linkrepair: настройка не прочиталась", "router_id", routerID, "tunnel_id", tunnelID, "err", err)
			}
			return linkrepair.Setting{}, false
		}
		if !ok {
			return linkrepair.Setting{}, false
		}
		return linkrepair.Setting{Enabled: s.Enabled, Provider: s.Provider, Option: s.Option, AllowRelocate: s.AllowRelocate, TunnelName: s.TunnelName, RelocateSpent: s.RelocateSpent}, true
	}
}

// LinkRepairDropSetting удаляет настройку автопочинки VPN-туннеля: он удалён
// или списан заменой. Ошибка -- только в лог: худшее, что останется, --
// строка, у которой имя не совпадёт, и движок попросит подтвердить её заново.
func LinkRepairDropSetting(database *db.DB, logger *slog.Logger) func(routerID int64, tunnelID string) {
	return func(routerID int64, tunnelID string) {
		if err := database.TunnelRepairSettings().Delete(routerID, tunnelID); err != nil && logger != nil {
			logger.Warn("linkrepair: настройка не удалилась", "router_id", routerID, "tunnel_id", tunnelID, "err", err)
		}
	}
}

// LinkRepairSpendRelocation -- отметка «новая страна выпущена» в настройке
// VPN-туннеля. Ошибка -- движок страну не выпускает.
func LinkRepairSpendRelocation(database *db.DB) func(routerID int64, tunnelID, option string) error {
	return func(routerID int64, tunnelID, option string) error {
		return database.TunnelRepairSettings().SpendRelocation(routerID, tunnelID, option)
	}
}

// LinkRepairSaveOption -- удачная смена локации: новый вариант запоминается
// в настройке туннеля (остальные поля как были; строки нет -- не заводится,
// тумблер сам не включается) и в происхождении конфига -- на роутере теперь
// конфиг этого варианта.
func LinkRepairSaveOption(database *db.DB, logger *slog.Logger) func(routerID int64, tunnelID, provider, option string) {
	warn := func(msg string, err error, routerID int64, tunnelID string) {
		if logger != nil {
			logger.Warn(msg, "router_id", routerID, "tunnel_id", tunnelID, "err", err)
		}
	}
	return func(routerID int64, tunnelID, provider, option string) {
		repo := database.TunnelRepairSettings()
		s, ok, err := repo.Get(routerID, tunnelID)
		if err != nil {
			warn("linkrepair: настройка не прочиталась", err, routerID, tunnelID)
		} else if ok && s.Provider == provider {
			s.Option = option
			if err := repo.Put(s); err != nil {
				warn("linkrepair: новая локация не записалась в настройку", err, routerID, tunnelID)
			}
		}
		name := ""
		if o, found, err := database.TunnelOrigins().Get(routerID, tunnelID); err == nil && found {
			name = o.TunnelName
		}
		if err := database.TunnelOrigins().Record(routerID, tunnelID, name, provider, option, time.Now(), 0); err != nil {
			warn("linkrepair: происхождение конфига не записалось", err, routerID, tunnelID)
		}
	}
}

// LinkRepairSaveUnconfirmed -- другая локация легла на роутер, но проверку
// не прошла: настройка (если её провайдер тот же) и происхождение всё равно
// указывают на неё -- на роутере теперь её конфиг. Происхождение -- с
// отметкой «не подтверждена». Иначе следующая починка «тот же конфиг на
// месте» выпустила бы старую страну, а экран врал бы, чем поднят туннель.
func LinkRepairSaveUnconfirmed(database *db.DB, logger *slog.Logger) func(routerID int64, tunnelID, provider, option string) {
	warn := func(msg string, err error, routerID int64, tunnelID string) {
		if logger != nil {
			logger.Warn(msg, "router_id", routerID, "tunnel_id", tunnelID, "err", err)
		}
	}
	return func(routerID int64, tunnelID, provider, option string) {
		repo := database.TunnelRepairSettings()
		s, ok, err := repo.Get(routerID, tunnelID)
		if err != nil {
			warn("linkrepair: настройка не прочиталась", err, routerID, tunnelID)
		} else if ok && s.Provider == provider {
			s.Option = option
			if err := repo.Put(s); err != nil {
				warn("linkrepair: локация на роутере не записалась в настройку", err, routerID, tunnelID)
			}
		}
		name := ""
		if o, found, err := database.TunnelOrigins().Get(routerID, tunnelID); err == nil && found {
			name = o.TunnelName
		}
		if err := database.TunnelOrigins().RecordUnconfirmed(routerID, tunnelID, name, provider, option, time.Now()); err != nil {
			warn("linkrepair: происхождение конфига не записалось", err, routerID, tunnelID)
		}
	}
}

// deletedTunnelID -- какой VPN-туннель удаляла команда tunnel_delete: по
// tunnel_id или (мастер) по проверке tunnel_<id>.
func deletedTunnelID(args map[string]any) string {
	if tid, _ := args["tunnel_id"].(string); strings.TrimSpace(tid) != "" {
		return strings.TrimSpace(tid)
	}
	check, _ := args["check_name"].(string)
	if tid, ok := strings.CutPrefix(strings.TrimSpace(check), "tunnel_"); ok {
		return strings.TrimSpace(tid)
	}
	return ""
}
