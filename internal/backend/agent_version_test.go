package backend

import "testing"

// Гейт по версии агента -- ВСТРЕЧНАЯ к replace.MinAgentVersion логика, и
// разница в одном: там пустая и нечитаемая версия ПРОПУСКАЕТСЯ (мастер и без
// того умеет падать с откатом), а здесь -- ЗАПРЕЩАЕТ действие.
//
// Причина в цене ошибки. Старый агент не знает про новые поля и сделает не
// то, что человек прочитал на экране: правка конфига у него перезапишет
// config.yaml по своим правилам и перезапустит агента. «Не знаю версию»
// здесь означает «не знаю, что случится», и это не повод делать.
func TestAgentAtLeastIsFailClosed(t *testing.T) {
	for _, tc := range []struct {
		version string
		want    bool
	}{
		{"v0.31.0", true}, {"v0.31.1", true}, {"v0.32.0", true},
		// Без префикса -- та же версия: роутеры сообщают и так, и так.
		{"0.31.0", true},
		{"v0.30.1", false}, {"v0.18.0", false},
		// Предрелиз ниже релиза по semver: v0.31.0-rc1 < v0.31.0.
		{"v0.31.0-rc1", false},
		{"", false},      // не сообщал -- ЗАПРЕЩАЕМ (в отличие от replace)
		{"мусор", false}, // не разобрали -- ЗАПРЕЩАЕМ
		{"v0.31", false}, // не semver -- ЗАПРЕЩАЕМ, а не «похоже, хватит»
	} {
		if got := agentAtLeast(tc.version, "v0.31.0"); got != tc.want {
			t.Errorf("agentAtLeast(%q, v0.31.0) = %v, want %v", tc.version, got, tc.want)
		}
	}
	// Нечитаемый пол -- тоже отказ: опечатка в собственной константе не
	// должна открывать действие всему парку.
	if agentAtLeast("v0.31.0", "мусор") {
		t.Error("agentAtLeast с нечитаемым полом разрешила действие")
	}
}

// Набор «только админу» и пол версии -- не общий список «опасного», а два
// разных вопроса: кто нажимает и что на роутере это выполнит. Чтение конфига
// агент умеет с давних версий, поэтому пола у него нет вовсе; запись
// перезапускает агента, и её пол есть.
func TestMiniappAgentConfigSetsAreDeliberate(t *testing.T) {
	if !miniappAdminOnlyActions["agent_config_get"] || !miniappAdminOnlyActions["update_agent_config"] {
		t.Error("правка конфига агента обязана быть только для админа")
	}
	if miniappActionMinAgentVersion["update_agent_config"] == "" {
		t.Error("у записи конфига агента обязан быть пол версии")
	}
	if _, floored := miniappActionMinAgentVersion["agent_config_get"]; floored {
		t.Error("чтение конфига агент умеет с давних версий: пол версии закрыл бы экран исправным роутерам")
	}
}
