// Package wire — maintenance.go defines payload types for version_audit and
// firmware_status commands. They are JSON-encoded into wire.CommandResult.Output;
// no wire envelope additions are required besides the action names.
package wire

// VersionAudit is the agent's compact reply to a version_audit command.
// Backend uses it to render the Maintenance panel and to compute soft-warning
// updates for the smart-reply.
type VersionAudit struct {
	AwgmgrVersion string `json:"awgmgr_version"`
	AwgmgrBackend string `json:"awgmgr_backend,omitempty"`
	AwgmgrRunning bool   `json:"awgmgr_running,omitempty"`
	// HrneoInstalled -- указатель по той же причине, что и KmodLoaded: опрос
	// HydraRoute может не дать ответа, и тогда это nil («не знаем»), а не
	// false («не установлен»). Пока здесь был обычный bool, один неудачный
	// опрос затирал в снимке ранее известное «установлен», и экран потом
	// устойчиво врал владельцу, у которого HydraRoute стоит и работает.
	//
	// Старую форму это не ломает. Агент с обычным bool и omitempty присылал
	// true только когда HydraRoute действительно стоит -- такой true доезжает
	// указателем; а отсутствие поля у него и раньше значило «не установлен
	// ИЛИ опрос не удался», то есть ровно nil.
	HrneoInstalled  *bool  `json:"hrneo_installed,omitempty"`
	HrneoRunning    bool   `json:"hrneo_running,omitempty"`
	HrneoVersion    string `json:"hrneo_version,omitempty"`
	FirmwareCurrent string `json:"firmware_current"`
	FirmwareAvail   string `json:"firmware_avail,omitempty"`
	HrneoUptime     string `json:"hrneo_uptime,omitempty"`
	AwgmgrUptime    string `json:"awgmgr_uptime,omitempty"`
	// Модуль ядра AmneziaWG. Обновление панели может его сменить, и тогда
	// VPN-туннели поднимутся только после перезагрузки роутера -- значит
	// сравнивать «было/стало» по нему надо, а раньше было нечем: данные
	// лежали в SystemInfo, а в этот ответ не переносились.
	KmodVersion string `json:"kmod_version,omitempty"`
	KmodModel   string `json:"kmod_model,omitempty"`
	// KmodLoaded -- указатель, потому что nil («агент старый и не сказал») и
	// false («не загружен») -- разные ответы, и второй означает поломку.
	KmodLoaded *bool `json:"kmod_loaded,omitempty"`
	// KmodLoadedVersion -- версия модуля, которую держит ядро прямо сейчас.
	// Расходится с KmodVersion -- модуль сменили, а ядро держит старый:
	// VPN-туннели поднимутся только после перезагрузки роутера.
	KmodLoadedVersion string `json:"kmod_loaded_version,omitempty"`
}

// FirmwareStatus is the agent's reply to a firmware_status command.
// Mirrors the relevant fields from `ndmc -c "components list"` (firmware
// vs local sub-blocks).
type FirmwareStatus struct {
	Current   string `json:"current"`
	Available string `json:"available,omitempty"`
	Hint      string `json:"hint,omitempty"`
	Channel   string `json:"channel,omitempty"`
}

// OpkgCronStatus is returned by opkg_cron_* commands. It is intentionally
// router-truth shaped: the dashboard renders these fields directly after a
// fresh command result instead of persisting stale install state in backend DB.
type OpkgCronStatus struct {
	Installed   bool   `json:"installed"`
	Schedule    string `json:"schedule,omitempty"`
	ScriptPath  string `json:"script_path"`
	CronPath    string `json:"cron_path,omitempty"`
	LogPath     string `json:"log_path"`
	CronService string `json:"cron_service,omitempty"`
	FreeKB      int64  `json:"free_kb,omitempty"`
	TotalKB     int64  `json:"total_kb,omitempty"`
	MinFreeKB   int64  `json:"min_free_kb,omitempty"`
	LastRun     string `json:"last_run,omitempty"`
	LastStatus  string `json:"last_status,omitempty"`
	LogTail     string `json:"log_tail,omitempty"`
}

// EntwareCleanStatus is returned by entware_clean_* commands. It mirrors live
// router state after the managed cleanup script is installed, run, queried, or
// removed.
type EntwareCleanStatus struct {
	Installed         bool   `json:"installed"`
	Schedule          string `json:"schedule,omitempty"`
	ScriptPath        string `json:"script_path"`
	CronPath          string `json:"cron_path,omitempty"`
	LogPath           string `json:"log_path"`
	CronService       string `json:"cron_service,omitempty"`
	FreeKB            int64  `json:"free_kb,omitempty"`
	TotalKB           int64  `json:"total_kb,omitempty"`
	MinFreeKB         int64  `json:"min_free_kb,omitempty"`
	MemAvailableKB    int64  `json:"mem_available_kb,omitempty"`
	MemTotalKB        int64  `json:"mem_total_kb,omitempty"`
	MinMemAvailableKB int64  `json:"min_mem_available_kb,omitempty"`
	LastRun           string `json:"last_run,omitempty"`
	LastStatus        string `json:"last_status,omitempty"`
	LastFreedKB       int64  `json:"last_freed_kb,omitempty"`
	LogTail           string `json:"log_tail,omitempty"`
}

// TunnelTraffic -- ответ на tunnel_traffic: обмен по одному туннелю за период,
// каким его ведёт сам роутер (/api/tunnels/traffic).
//
// RXTotal/TXTotal -- ОБЪЁМ за период, и считает его роутер (stats.volumeRx /
// volumeTx: «Σ rxRate×Δt на сырых отсчётах»). Агент его только переносит.
// Своя сумма по точкам была бы вторым мнением о том же числе -- и неверным:
// точки несут скорости, а не байты.
//
// Пустой Points при status ok -- это ответ «обмена не было», а не сбой: экран
// обязан написать 0, а не «неизвестно».
type TunnelTraffic struct {
	TunnelID string `json:"tunnel_id"`
	Period   string `json:"period,omitempty"`
	RXTotal  int64  `json:"rx_total"`
	TXTotal  int64  `json:"tx_total"`
	// CurrentRx/CurrentTx -- мгновенная скорость на последнем отсчёте, байт/сек.
	CurrentRx float64        `json:"current_rx,omitempty"`
	CurrentTx float64        `json:"current_tx,omitempty"`
	Points    []TrafficPoint `json:"points"`
}

// TrafficPoint -- момент (unix-секунды) и две СКОРОСТИ в байтах в секунду.
// Не счётчики: складывать их нельзя, объём лежит в TunnelTraffic.RXTotal.
type TrafficPoint struct {
	T  int64   `json:"t"`
	RX float64 `json:"rx"`
	TX float64 `json:"tx"`
}

// AwgmUpdateResult -- ответ действия awgm_update (JSON в CommandResult.Output).
type AwgmUpdateResult struct {
	Updated       bool   `json:"updated"`
	From          string `json:"from"`
	To            string `json:"to"`
	KmodInstalled string `json:"kmod_installed"`
	KmodLoaded    string `json:"kmod_loaded"`
	RebootNeeded  bool   `json:"reboot_needed"`
}

// HrneoUpdateResult -- ответ действия hrneo_update (JSON в CommandResult.Output).
type HrneoUpdateResult struct {
	Updated bool   `json:"updated"`
	From    string `json:"from"`
	To      string `json:"to"`
	Running bool   `json:"running"`
}

// RebootNeeded -- единое правило «нужна перезагрузка роутера»: обе версии
// модуля ядра известны и расходятся. Им пользуются и агент (awgm_update), и
// бэкенд (upstream.RebootHint), чтобы правило не разъехалось на две копии.
func RebootNeeded(installed, loaded string) bool {
	return installed != "" && loaded != "" && installed != loaded
}

// PorthopStatus -- ответ porthop_status/install/remove/logs (v0.57): смена
// исходящего порта VPN-туннеля, чей поток убила блокировка.
//
// Auto -- настройка «сторожить все VPN-туннели с маршрутом 0.0.0.0/0»; тогда
// Ifaces пуст. Отдельным полем, а не строкой "auto" в списке: имя интерфейса
// "auto" допустимо. Watched -- что скрипт сторожит СЕЙЧАС, вычислено тем же
// правилом, что в скрипте; без установки -- что сторожил бы auto.
//
// Счёт за 24 часа -- по журналу скрипта: Hops24h = Recovered24h + Failed24h.
type PorthopStatus struct {
	Installed    bool          `json:"installed"`
	Running      bool          `json:"running"`
	Auto         bool          `json:"auto"`
	Ifaces       []string      `json:"ifaces,omitempty"`
	Watched      []string      `json:"watched"`
	Legacy       PorthopLegacy `json:"legacy"`
	Hops24h      int           `json:"hops_24h"`
	Recovered24h int           `json:"recovered_24h"`
	Failed24h    int           `json:"failed_24h"`
	// LastEvent -- последняя строка журнала как есть («2026-10-07 12:00:00
	// +0300 opkgtun10: порт 30000 -> 41234, поток ожил (хендшейк 3 с)»;
	// строки прежней версии -- без смещения).
	LastEvent  string `json:"last_event,omitempty"`
	LogTail    string `json:"log_tail,omitempty"`
	ScriptPath string `json:"script_path"`
	ConfPath   string `json:"conf_path"`
	LogPath    string `json:"log_path"`
}

// PorthopLegacy -- ручная копия оператора (/opt/etc/init.d/S99awg-porthop
// или процесс awg-porthop.sh не из нашего пути). Две копии дрались бы за
// один интерфейс.
//
// Found -- копия запустится при загрузке (init на месте) или работает.
// MovedTo -- куда агент перенёс её init при замене (replace_legacy): вернуть
// ручную копию -- перенести файл обратно в /opt/etc/init.d.
type PorthopLegacy struct {
	Found   bool   `json:"found"`
	Path    string `json:"path,omitempty"`
	Running bool   `json:"running"`
	MovedTo string `json:"moved_to,omitempty"`
}

// SpaceReport -- ответ space_report (v0.57): место на /opt и крупнейшие
// каталоги (du -x -k -d 2 /opt, первые 10, без самого /opt). Только чтение.
type SpaceReport struct {
	FreeKB  int64        `json:"free_kb"`
	TotalKB int64        `json:"total_kb"`
	Top     []SpaceEntry `json:"top"`
}

// SpaceEntry -- один каталог в SpaceReport.
type SpaceEntry struct {
	Path string `json:"path"`
	KB   int64  `json:"kb"`
}

// DNSResetResult -- Payload ответа dns_reset (v0.57; Output остаётся
// транскриптом). Probes -- проба каждого сервера эталона настоящим
// DoT-запросом перед применением; агент старше v0.57 Payload не шлёт.
type DNSResetResult struct {
	Probes []DNSProbe `json:"probes,omitempty"`
}

// IsZero -- нечего прикладывать к ответу.
func (r DNSResetResult) IsZero() bool { return len(r.Probes) == 0 }

// DNSProbe -- проба одного сервера эталона. Server -- как в строке
// dns-proxy (IP или имя), Purpose -- "ru" (Яндекс, русские зоны) или
// "foreign". Error -- причина, когда OK=false.
type DNSProbe struct {
	Server  string `json:"server"`
	Purpose string `json:"purpose,omitempty"`
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
}
