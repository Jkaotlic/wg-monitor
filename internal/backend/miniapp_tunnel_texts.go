package backend

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// miniappTunnelTexts -- отказ словами для маршрутов VPN-туннелей цикла 4
// (удаление, импорт .conf). Общие коды (not_found, bad_json, internal,
// unsupported_content_type) берутся из таблицы кабинетов.
// #nosec G101 -- тексты отказов для человека, не учётные данные
var miniappTunnelTexts = map[string]string{
	"commands_not_configured": "Очередь команд роутерам на сервере не настроена",
	"invalid_tunnel_id":       "VPN-туннель не распознан — обновите экран",
	"router_failed":           "Роутер не отдал список правил — повторите через минуту",
	"router_garbled":          "Роутер прислал непонятный ответ — обновите агента и повторите",
	"tunnel_not_found":        "Такого VPN-туннеля на роутере уже нет — обновите экран",
	"tunnel_not_managed":      "Это подключение роутера, а не VPN-туннель awg-manager — удалить его отсюда нельзя",
	"confirm_mismatch":        "Имя VPN-туннеля набрано неверно",
	"snapshot_partial":        "Роутер отдал неполный список правил — удалять вслепую нельзя, повторите через минуту",
	"tunnel_is_default":       "Этот VPN-туннель — главный выход роутера: через него идёт всё, что не названо правилами. Сначала переключите главный выход в awg-manager",
	"tunnel_has_rules":        "На этом VPN-туннеле есть правила — сначала перенесите их на другой VPN-туннель",
	"invalid_name":            "Имя VPN-туннеля — строчные латинские буквы, цифры, «-» и «_», от 2 до 32 знаков, первой идёт буква",
	"invalid_conf":            "Это не конфиг WireGuard или AmneziaWG: нужны [Interface] с PrivateKey и [Peer] с PublicKey и Endpoint",
	"conf_too_large":          "Файл больше 50 КиБ — это не конфиг VPN-туннеля",
	"name_taken":              "VPN-туннель с таким именем на роутере уже есть — выберите другое имя",
	"preview_expired":         "Предпросмотр устарел — загрузите файл заново",
	"preview_not_ready":       "Роутер ещё проверяет конфиг — подождите несколько секунд",
	"conf_rejected":           "Роутер не примет этот конфиг — исправьте ошибки из проверки и загрузите файл заново",
}

func miniappTunnelErrorText(code string) string {
	if text, ok := miniappTunnelTexts[code]; ok {
		return text
	}
	return miniappCabinetErrorText(code)
}

func writeMiniappTunnelError(w http.ResponseWriter, status int, code string) {
	writeMiniappDeployError(w, status, code, miniappTunnelErrorText(code))
}

// miniappRulesCount -- «1 правило», «3 правила», «5 правил».
func miniappRulesCount(n int) string {
	word := "правил"
	m10, m100 := n%10, n%100
	switch {
	case m10 == 1 && m100 != 11:
		word = "правило"
	case m10 >= 2 && m10 <= 4 && (m100 < 12 || m100 > 14):
		word = "правила"
	}
	return fmt.Sprintf("%d %s", n, word)
}

// writeMiniappTunnelHasRules -- отказ с числом и разбивкой: экран показывает
// message как есть, а rules нужны кнопке «Перенести».
func writeMiniappTunnelHasRules(w http.ResponseWriter, rules miniappTunnelRules) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusConflict)
	_ = json.NewEncoder(w).Encode(struct {
		Code    string             `json:"code"`
		Error   string             `json:"error"`
		Message string             `json:"message"`
		Rules   miniappTunnelRules `json:"rules"`
	}{
		Code:    "tunnel_has_rules",
		Error:   "tunnel_has_rules",
		Message: fmt.Sprintf("На этом VPN-туннеле %s — сначала перенесите их на другой VPN-туннель", miniappRulesCount(rules.Total)),
		Rules:   rules,
	})
}
