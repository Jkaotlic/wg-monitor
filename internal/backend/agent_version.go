package backend

import (
	"strings"

	"golang.org/x/mod/semver"
)

// Гейт по версии агента для опасных действий мини-аппа.
//
// agentAtLeast -- ВСТРЕЧНАЯ к replace.MinAgentVersion логика: там пустая и
// нечитаемая версия ПРОПУСКАЕТСЯ (мастер и без того умеет падать с откатом),
// а здесь она ЗАПРЕЩАЕТ действие. Причина в цене ошибки: старый агент не
// знает про новые поля и сделает не то, что показано на экране, -- перепишет
// config.yaml по своим правилам и перезапустит себя. Отказ по умолчанию.
//
// Версия -- та, о которой роутер сообщил сам в последнем отчёте
// (users.last_seen_agent_version, см. miniapp_replace.go:117-119). Пол
// сравнивается целиком: «v0.31» без patch-части не разбираем намеренно --
// такую версию парк не сообщает, а догадка о patch-уровне и есть то, что
// отказ по умолчанию запрещает.
func agentAtLeast(version, floor string) bool {
	v, ok := agentSemver(version)
	if !ok {
		return false
	}
	f, ok := agentSemver(floor)
	if !ok {
		return false
	}
	return semver.Compare(v, f) >= 0
}

// agentSemver приводит версию агента к каноничному для x/mod/semver виду и
// говорит, разобралась ли она вообще. Требование трёх числовых частей --
// часть отказа по умолчанию: x/mod/semver считает «v0.31» годной и дополняет
// её нулями, то есть решает за нас то, чего мы не знаем.
func agentSemver(s string) (string, bool) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(strings.TrimPrefix(s, "v"), "V")
	if s == "" {
		return "", false
	}
	s = "v" + s
	if !semver.IsValid(s) {
		return "", false
	}
	core := s
	if i := strings.IndexAny(core, "-+"); i >= 0 {
		core = core[:i]
	}
	if strings.Count(core, ".") != 2 {
		return "", false
	}
	return s, true
}

// miniappActionMinAgentVersion -- пол версии агента для опасных действий
// мини-аппа. Бэкенд отказывается ставить команду в очередь агенту ниже него:
// это ПЕРВАЯ из двух независимых преград (решение оператора п. 10), вторая --
// экран, который для таких роутеров не рисуется вовсе.
//
// Пол стоит только у записи. Чтение конфига (agent_config_get) агент умеет с
// давних версий, и пол на нём закрыл бы экран исправным роутерам.
//
// dns_reset стоит здесь целиком, вместе с предпросмотром: его радиус
// router-global, а старый агент не знает про dry_run и на «посмотреть, что
// изменится» сделал бы настоящий сброс.
var miniappActionMinAgentVersion = map[string]string{
	"update_agent_config": "v0.31.0",
	"dns_reset":           "v0.31.0",
}

// miniappAdminOnlyActions -- действия, чей радиус router-global: их видит и
// нажимает только админ бота. Расширение круга на владельцев и операторов --
// отдельный пункт бэклога (п. 5), и до него отказ приходит как 404
// not_found, а не 403: владельцу роутера незачем узнавать по коду ответа,
// что действие вообще существует.
//
// Рядом с miniappOwnerOnlyActions намеренно: «кто нажимает» и «что это
// выполнит на роутере» -- два разных вопроса, и списки у них разные.
var miniappAdminOnlyActions = map[string]bool{
	"agent_config_get":    true,
	"update_agent_config": true,
	"dns_reset":           true,
}
