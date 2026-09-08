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
// блокировок, пока мы чиним. Починка идёт фоном, а failback возвращает его
// на ту же линию, ради которой всё затевалось.
package linkrepair

import (
	"strings"

	"github.com/Jkaotlic/wg-monitor/internal/backend/provision"
	"github.com/Jkaotlic/wg-monitor/internal/backend/replace"
)

// KindLinkRepair -- вид задания в общем Store. Имя не сокращается до
// "repair": provision.JobKind уже несёт repair_repoint и repair_reinstall,
// и это про починку УСТАНОВКИ агента, другой смысл.
const KindLinkRepair provision.JobKind = "link_repair"

// Свои у движка только два шага -- увести и вернуть. Всё между ними делает
// мастер замены своим чеклистом: перевыпуск конфига -- это он и есть.
const (
	StepFailover = "failover"
	StepFailback = "failback"
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

// Steps -- полный чеклист задания: увести на резерв, отработать замену
// конфига целиком, вернуться на починенную линию. Имена шагов замены берутся
// из её же пакета, чтобы Store.Update находил их по имени, когда мастер
// будет двигать их в том же задании.
func Steps() []provision.Step {
	steps := []provision.Step{{Name: StepFailover, Status: provision.StepPending}}
	steps = append(steps, replace.Steps()...)
	return append(steps, provision.Step{Name: StepFailback, Status: provision.StepPending})
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
