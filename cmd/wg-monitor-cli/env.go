package main

import (
	"os"
	"strings"
)

// envOr — значение переменной окружения либо запасной вариант. Нужна, чтобы
// боевой адрес бэкенда не лежал константой в исходниках публичного репозитория:
// подсказка по установке берёт его из WGM_BACKEND_URL, а дефолт остаётся
// документационным плейсхолдером.
func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}
