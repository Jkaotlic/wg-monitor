package backend

import (
	"net/http/httptest"
	"reflect"
	"testing"
)

// Раньше dns_reset стоял среди «команд без аргументов», и санитайзер вырезал
// клиентский ввод подчистую. Безопасно, но предпросмотр был невозможен в
// принципе: dry_run не доезжал до агента никогда. Теперь у команды своя ветка,
// и наружу уходит ровно одно поле.
func TestSanitizeDNSResetKeepsOnlyDryRun(t *testing.T) {
	cases := []struct {
		name string
		in   map[string]any
		want map[string]any
	}{
		{
			name: "чужие ключи не доезжают",
			in: map[string]any{
				"dry_run":    true,
				"ndms_name":  "Wireguard0",
				"url":        "https://example.com/evil",
				"keep_hosts": []any{"dns.example.com"},
			},
			want: map[string]any{"dry_run": true},
		},
		{
			name: "без dry_run -- настоящий сброс, как у кнопки дашборда сегодня",
			in:   map[string]any{},
			want: map[string]any{"dry_run": false},
		},
		{
			name: "явный false остаётся настоящим сбросом",
			in:   map[string]any{"dry_run": false},
			want: map[string]any{"dry_run": false},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			got, ok := sanitizeWizardCommandArgs(w, "dns_reset", c.in)
			if !ok {
				t.Fatalf("санитайзер отверг команду: %d %s", w.Code, w.Body.String())
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("аргументы = %#v, хотим %#v", got, c.want)
			}
		})
	}
}

// Значение неверного типа отвергается, а не приводится. `false` означало бы
// разрушительный сброс там, где клиент просил предпросмотр; `true` -- молчаливый
// отказ выполнить то, что человек нажал. Угадывать намерение сломанного клиента
// опаснее, чем отказать вслух.
func TestSanitizeDNSResetRejectsNonBoolDryRun(t *testing.T) {
	for _, bad := range []any{"true", 1, []any{true}, map[string]any{}} {
		w := httptest.NewRecorder()
		got, ok := sanitizeWizardCommandArgs(w, "dns_reset", map[string]any{"dry_run": bad})
		if ok {
			t.Errorf("dry_run=%#v принят, аргументы %#v — должен быть отказ", bad, got)
			continue
		}
		if w.Code != 400 {
			t.Errorf("dry_run=%#v: код %d, хотим 400", bad, w.Code)
		}
	}
}
