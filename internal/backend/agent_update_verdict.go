// internal/backend/agent_update_verdict.go
package backend

import (
	"strings"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

// agentSelfUpdateFloor -- ниже этой версии агент обновиться сам не умеет:
// только переустановка (оживление, цикл 2б).
const agentSelfUpdateFloor = "v0.13.0-rc32"

const (
	// До v0.14.4 проверка места хотела 64 МБ «под бинарь» плюс 10% раздела.
	agentSpaceCheck64MBBefore = "v0.14.4"
	// С v0.25.0 запас ограничен сверху; до неё -- 10% раздела.
	agentSpaceCheckFixedIn = "v0.25.0"
	// С v0.14.0 агент сверяет адрес загрузки со своим адресом бэкенда.
	agentRepoBaseCheckSince = "v0.14.0"
)

type agentUpdateVerdict struct {
	Behind  bool
	TooOld  bool
	Unknown bool
	Warning string
}

// agentUpdateVerdictFor -- приговор по версии агента против версии бэкенда.
// Сравнение -- порядком тегов проекта (compareDashboardReleaseTags), где
// rc-номер -- число: x/mod/semver поставил бы rc4 выше rc32.
func agentUpdateVerdictFor(agentVersion, backendVersion string) agentUpdateVerdict {
	agentVersion = strings.TrimSpace(agentVersion)
	if _, ok := parseDashboardReleaseTagRank(agentVersion); !ok {
		return agentUpdateVerdict{Unknown: true}
	}
	var v agentUpdateVerdict
	v.TooOld = compareDashboardReleaseTags(agentVersion, agentSelfUpdateFloor) < 0
	if v.TooOld {
		// Ниже agentSelfUpdateFloor у агента нет self_update вовсе --
		// Behind остаётся false: иначе /fleet считает такой роутер
		// «отстающим», кнопка «Обновить» и счётчик «Обновить всех
		// отставших (N)» обещают то, что кончится отказом agent_too_old
		// (B6). Вместо этого -- прямое предупреждение: нужна переустановка.
		v.Warning = "агент слишком старый — нужна переустановка"
		return v
	}
	if _, ok := parseDashboardReleaseTagRank(strings.TrimSpace(backendVersion)); ok {
		v.Behind = compareDashboardReleaseTags(agentVersion, strings.TrimSpace(backendVersion)) < 0
	}
	if !v.Behind {
		return v
	}
	var warn []string
	switch {
	case compareDashboardReleaseTags(agentVersion, agentSpaceCheck64MBBefore) < 0:
		warn = append(warn, "старая проверка места: нужно 64 МБ и ещё ≈10% раздела /opt свободными")
	case compareDashboardReleaseTags(agentVersion, agentSpaceCheckFixedIn) < 0:
		warn = append(warn, "старая проверка места: нужно ≈10% раздела /opt свободно")
	}
	if compareDashboardReleaseTags(agentVersion, agentRepoBaseCheckSince) >= 0 {
		warn = append(warn, "проверяет адрес загрузки: он должен совпасть с адресом бэкенда в настройках агента")
	}
	v.Warning = strings.Join(warn, "; ")
	return v
}

// agentLongSilentAfter -- сколько молчания делает отстающий агент «давно не
// обновлявшимся» (v0.45, спека задачи B, п. 2).
const agentLongSilentAfter = 30 * 24 * time.Hour

// agentLongNotUpdated -- единственный на сервере признак «давно не
// обновлялся»: агент ниже agentSelfUpdateFloor (self_update не умеет вовсе)
// ИЛИ роутер молчит дольше 30 дней, а агент отстаёт от бэкенда. Такой роутер
// авто-проход оживляет переустановкой (auto_revive.go), строка парка
// объясняет, почему не может. Неизвестная версия -- не признак: судить не по
// чему. Ни разу не выходивший на связь -- тоже: это новый, а не забытый.
func agentLongNotUpdated(u *db.User, backendVersion string, now time.Time) bool {
	if u == nil {
		return false
	}
	verdict := agentUpdateVerdictFor(stringValue(u.LastDeployedVersion), backendVersion)
	if verdict.TooOld {
		return true
	}
	return verdict.Behind && u.LastSeenAt != nil && now.Sub(u.LastSeenAt.UTC()) > agentLongSilentAfter
}
