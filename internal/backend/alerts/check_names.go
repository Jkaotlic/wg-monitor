package alerts

import (
	_ "embed"
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// Одна таблица подписей проверок -- check_names.json: имена существительными
// («Определение адресов сайтов»), одни и те же в тревогах бота, на экранах
// «Роутер» и «Проверки» мини-аппа (он читает тот же файл при сборке). Своих
// словарей у экранов нет; тест «ровно одна запись на ключ проверки агента» --
// check_names_test.go. Динамические tunnel_<id> сюда не входят: их зовут по
// имени VPN-туннеля.
//
//go:embed check_names.json
var checkNamesJSON []byte

// CheckNames -- ключ проверки -> подпись.
var CheckNames = func() map[string]string {
	m := map[string]string{}
	if err := json.Unmarshal(checkNamesJSON, &m); err != nil {
		panic("alerts: check_names.json: " + err.Error())
	}
	return m
}()

// lowerFirst -- подпись внутри предложения: «Сервер имён…» -> «сервер имён…».
func lowerFirst(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	if r == utf8.RuneError {
		return s
	}
	return strings.ToLower(string(r)) + s[n:]
}
