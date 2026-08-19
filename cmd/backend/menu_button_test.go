package main

import "testing"

// Кнопка меню в приватном чате с ботом ведёт в мини-апп, когда он есть, и
// остаётся списком команд, когда его нет. Урл собирается тут, а не по месту
// вызова, чтобы форма пути была одна на весь бэкенд: тот же `/miniapp/`, что
// у web_app-кнопки в тревогах (alerts/dispatcher.go).
func TestMiniAppMenuURL(t *testing.T) {
	cases := []struct {
		name string
		base string
		want string
	}{
		{"обычный базовый урл", "https://wg.example.test", "https://wg.example.test/miniapp/"},
		{"лишний слэш на конце не удваивается", "https://wg.example.test/", "https://wg.example.test/miniapp/"},
		{"пробелы по краям не ломают путь", "  https://wg.example.test  ", "https://wg.example.test/miniapp/"},
		// Пустой public_base_url -- это "мини-аппа снаружи нет", и кнопку
		// вешать не на что.
		{"без базового урла кнопки нет", "", ""},
		// Telegram открывает web_app только по HTTPS: http-кнопка не заработала
		// бы, а молча стояла бы мёртвой. Лучше оставить список команд.
		{"http отвергается -- web_app бывает только по https", "http://wg.example.test", ""},
		{"схему без http(s) не принимаем", "wg.example.test", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := miniAppMenuURL(tc.base); got != tc.want {
				t.Fatalf("miniAppMenuURL(%q) = %q, want %q", tc.base, got, tc.want)
			}
		})
	}
}
