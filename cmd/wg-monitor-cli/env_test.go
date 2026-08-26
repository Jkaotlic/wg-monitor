package main

import "testing"

func TestEnvOrPrefersEnvironmentAndIgnoresBlank(t *testing.T) {
	const key = "WGM_TEST_ENV_OR"

	t.Setenv(key, "https://from.env.example")
	if got := envOr(key, "https://fallback.example"); got != "https://from.env.example" {
		t.Fatalf("значение из окружения проигнорировано: %q", got)
	}

	// Пробел — это не заданное значение. Иначе пустая переменная в systemd-юните
	// молча подменила бы адрес пустой строкой.
	t.Setenv(key, "   ")
	if got := envOr(key, "https://fallback.example"); got != "https://fallback.example" {
		t.Fatalf("пробельное значение принято за заданное: %q", got)
	}

	t.Setenv(key, "")
	if got := envOr(key, "https://fallback.example"); got != "https://fallback.example" {
		t.Fatalf("пустое значение принято за заданное: %q", got)
	}

	// Обрамляющие пробелы обязаны срезаться, а не уезжать в URL: адрес попадает
	// в подсказку по установке, и " https://x " там превращается в битую ссылку.
	t.Setenv(key, "  https://padded.example  ")
	if got := envOr(key, "https://fallback.example"); got != "https://padded.example" {
		t.Fatalf("значение не обрезано: %q", got)
	}
}
