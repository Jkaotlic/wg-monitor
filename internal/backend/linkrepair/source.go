package linkrepair

import (
	"context"

	"github.com/Jkaotlic/wg-monitor/internal/backend/replace"
)

// Source -- откуда пересоздавать конфиг. Реализация живёт в пакете backend:
// кабинеты провайдеров и панели своего сервера известны там, а движок про
// пакет backend не знает (иначе получился бы цикл).
type Source interface {
	// Issue -- тот же конфиг: тот же вариант у того же кабинета (слот не тратится).
	Issue(ctx context.Context, routerID int64, provider, option string) (replace.Issued, error)
	// Fresh -- пересоздать: awg3 -- новый пир; amnezia/hidemyname -- то же, что Issue.
	Fresh(ctx context.Context, routerID int64, provider, option string) (replace.Issued, error)
	// Options -- варианты кабинета по порядку (страны/серверы); для awg3 -- nil.
	Options(ctx context.Context, routerID int64, provider string) ([]Option, error)
}

// Option -- вариант кабинета. Label -- подпись кабинета («Германия»), её и
// читает владелец; Issued -- конфиг этого варианта уже выпущен и места в
// подписке больше не займёт.
type Option struct {
	ID     string
	Label  string
	Issued bool
}

// NeedHuman -- провал, после которого нужен человек, и что именно ему сделать.
// Action -- фраза для владельца (без латиницы вне ёлочек).
type NeedHuman struct {
	Cause  error
	Action string
}

func (n *NeedHuman) Error() string { return n.Cause.Error() }
func (n *NeedHuman) Unwrap() error { return n.Cause }

// Готовые фразы действий (спека, раздел 5). Названия кабинетов -- в ёлочках:
// латиница вне них владельцу не показывается.
const (
	ActAmneziaKey = "обновите ключ «Amnezia Premium» во вкладке «Управление»"
	ActHideMyCode = "продлите или замените код «HideMy.name» во вкладке «Управление»"
	ActNoSource   = "выберите, откуда пересоздавать, в настройке автопочинки этого VPN-туннеля"
	ActServerDead = "сервер не отвечает — смените сервер или разрешите автопочинке менять локацию"
	ActTooOften   = "VPN-туннель падает раз за разом — нужна ручная проверка"
	ActAgentOld   = "обновите агента во вкладке «Управление»"
	// ActNoNewCountry -- у «Amnezia Premium» все страны подписки уже выпущены:
	// их ключи стоят на других устройствах или в других VPN-туннелях, и взять
	// такой -- сломать оба места.
	ActNoNewCountry = "все страны «Amnezia Premium» уже выпущены и заняты в других местах — смените сервер во вкладке «Управление»"
	// ActRelocateSpent -- одну новую страну автопочинка этого VPN-туннеля уже
	// выпускала: вторая -- ещё одно место в подписке, без человека нельзя.
	ActRelocateSpent = "новую страну автопочинка уже выпускала один раз — смените сервер во вкладке «Управление»"
	// ActAborted -- лесенку прервал перезапуск бэкенда: вердикта нет, её
	// можно просто запустить снова.
	ActAborted = "починка прервана — бэкенд перезапускался; запустите её ещё раз"
	// ActRelocateNoHelp -- менять локацию было разрешено, и это не помогло.
	ActRelocateNoHelp = "другие локации тоже не помогли — смените сервер во вкладке «Управление»"
)

// ActRenamed -- VPN-туннель переименовали после включения автопочинки:
// выпускать конфиг без нового согласия нельзя.
func ActRenamed(name string) string {
	return "VPN-туннель теперь называется «" + name + "» — подтвердите автопочинку в приложении"
}

// ActVPSPanel -- действие, когда не ответила панель своего сервера.
func ActVPSPanel(panel string) string {
	return "проверьте свой сервер «" + panel + "»"
}
