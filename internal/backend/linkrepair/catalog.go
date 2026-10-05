// Package linkrepair -- движок починки упавшей линии.
//
// Порядок шагов -- не вкус, а следствие двух фактов о флоте:
//
//  1. У opkg-туннелей автофолбэка нет: NDMS-профиль ping-check awg-manager
//     заводит только для nativewg, а смерть удалённой стороны при живом
//     интерфейсе для NDMS невидима. Политика сама никуда не уйдёт.
//  2. Механизм фолбэка при этом исправен -- не хватает того, кто уведёт
//     трафик. Движок и есть этот «кто-то».
//
// Поэтому первым шагом идёт failover: человек не должен сидеть без обхода
// блокировок, пока мы чиним. Дальше -- лесенка: перезапуск, тот же конфиг на
// месте, пересоздание; failback возвращает трафик только на VPN-туннель,
// чья ступень доказана. Провал всех ступеней оставляет трафик на резерве.
package linkrepair

import (
	"strings"

	"github.com/Jkaotlic/wg-monitor/internal/backend/provision"
)

// KindLinkRepair -- вид задания в общем Store. Имя не сокращается до
// "repair": provision.JobKind уже несёт repair_repoint и repair_reinstall,
// и это про починку УСТАНОВКИ агента, другой смысл.
const KindLinkRepair provision.JobKind = "link_repair"

// Шаги лесенки (спека v0.54, раздел 3). Каждая ступень между уводом и
// возвратом засчитывается только доказанной: свежий обмен ключами и выход
// через VPN-туннель.
const (
	StepFailover = "failover" // 0. увести трафик на резерв
	StepRestart  = "restart"  // 1. поднять: перезапуск
	StepReissue  = "reissue"  // 2. тот же конфиг заново, в тот же VPN-туннель
	StepRecreate = "recreate" // 3. пересоздать: новый пир / другая локация
	StepFailback = "failback" // 4. вернуть трафик на починенный VPN-туннель
)

// tunnelCheckPrefix -- тот же префикс, которым агент называет проверку
// туннеля (internal/agent/checks/tunnels.go). Имя проверки "tunnel_awg12"
// несёт идентификатор туннеля, и это единственный способ узнать, какую
// линию чинить.
const tunnelCheckPrefix = "tunnel_"

// Scenario -- что и на какой линии чинить.
type Scenario struct {
	CheckName string
	Steps     []provision.Step
	TunnelID  string
}

// Steps -- полный чеклист задания: лесенка целиком, по порядку.
func Steps() []provision.Step {
	names := []string{StepFailover, StepRestart, StepReissue, StepRecreate, StepFailback}
	steps := make([]provision.Step, 0, len(names))
	for _, n := range names {
		steps = append(steps, provision.Step{Name: n, Status: provision.StepPending})
	}
	return steps
}

// ScenarioFor возвращает сценарий починки для провалившейся проверки.
// Второе значение false -- «отсюда это не чинится», и это нормальный
// ответ, а не ошибка: у пропавшего интернета и молчащего роутера нет
// шага, который движок мог бы выполнить.
func ScenarioFor(checkName string) (Scenario, bool) {
	id, ok := strings.CutPrefix(checkName, tunnelCheckPrefix)
	if !ok || id == "" {
		return Scenario{}, false
	}
	return Scenario{CheckName: checkName, Steps: Steps(), TunnelID: id}, true
}
