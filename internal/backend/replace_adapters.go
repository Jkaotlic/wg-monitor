package backend

import (
	"context"
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

type linkRepairOrigin struct{ db *db.DB }

// LinkRepairOrigin читает происхождение конфига для движка починки: чем была
// поднята упавшая линия, тем её и перевыпускаем. Отсутствие строки -- это
// «система не помнит», а не ошибка: у линий, заведённых руками или до мастера
// замены, происхождения нет, и выдумать его нечем.
func LinkRepairOrigin(database *db.DB) linkrepair.OriginReader { return linkRepairOrigin{db: database} }

func (o linkRepairOrigin) Get(routerID int64, tunnelID string) (string, string, bool) {
	got, ok, err := o.db.TunnelOrigins().Get(routerID, tunnelID)
	if err != nil || !ok {
		return "", "", false
	}
	return got.Provider, got.Variant, true
}
