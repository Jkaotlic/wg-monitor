package replace

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// Проверки мастера замены, вынесенные для лесенки автопочинки: те же
// критерии «получилось», что и у мастера, без задания и шагов на экране.

// AnalyzeConf спрашивает роутер (awg-manager 2.18.x), примет ли модуль этот
// конфиг, — до импорта. skipped=true: роутер анализ не умеет (старый агент
// или старая панель) -- это не ошибка. err != nil: роутер примет конфиг с
// ошибками. detail -- описание для шага на экране.
func (d Deps) AnalyzeConf(ctx context.Context, routerID int64, conf []byte) (detail string, skipped bool, err error) {
	const skippedText = "проверку конфига этот роутер пока не умеет — пропускаю"
	res, cerr := d.command(ctx, routerID, "tunnel_analyze", map[string]any{
		"conf": base64.StdEncoding.EncodeToString(conf),
	})
	if cerr != nil {
		return skippedText, true, nil
	}
	var out struct {
		Supported bool           `json:"supported"`
		Errors    []analyzeIssue `json:"errors"`
		Warnings  []analyzeIssue `json:"warnings"`
	}
	if json.Unmarshal([]byte(res.Output), &out) != nil || !out.Supported {
		return skippedText, true, nil
	}
	if len(out.Errors) > 0 {
		msgs := joinIssues(out.Errors)
		return "роутер не примет этот конфиг: " + msgs, false, errors.New("роутер не примет выпущенный конфиг: " + msgs)
	}
	if len(out.Warnings) > 0 {
		return "конфиг подходит, но есть замечания: " + joinIssues(out.Warnings), false, nil
	}
	return "конфиг подходит модулю роутера", false, nil
}

// analyzeIssue — ошибка или замечание анализа. Message awg-manager пишет
// по-русски и для человека, поэтому оно уходит владельцу как есть.
type analyzeIssue struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func joinIssues(issues []analyzeIssue) string {
	msgs := make([]string, 0, len(issues))
	for _, is := range issues {
		if m := strings.TrimSpace(is.Message); m != "" {
			msgs = append(msgs, m)
		} else if is.Code != "" {
			msgs = append(msgs, is.Code)
		}
	}
	return strings.Join(msgs, "; ")
}

// WaitHandshake ищет линию в снимке по идентификатору, а в текст для человека
// кладёт её имя.
func (d Deps) WaitHandshake(ctx context.Context, routerID int64, tunnelID, name string) error {
	last := ""
	for i := 0; i < d.handshakeTries(); i++ {
		res, err := d.command(ctx, routerID, "route_status", map[string]any{})
		if err != nil {
			return fmt.Errorf("не узнали у роутера, обменялся ли новый VPN-туннель ключами: %w", err)
		}
		var snap wire.RouteSnapshot
		if err := json.Unmarshal([]byte(res.Output), &snap); err != nil {
			d.logCommandFailure("route_status", "unparsable", err.Error())
			return errors.New("не узнали у роутера, обменялся ли новый VPN-туннель ключами: " + RouterGarbled)
		}
		for _, t := range snap.Tunnels {
			if t.ID != tunnelID {
				continue
			}
			if t.HasHandshake {
				return nil
			}
			last = fmt.Sprintf("VPN-туннель «%s» на роутере есть, но ключами ещё не обменялся", name)
		}
		if last == "" {
			last = fmt.Sprintf("VPN-туннеля «%s» на роутере не видно", name)
		}
		d.sleep(ctx, d.handshakeWait())
	}
	return errors.New("новый VPN-туннель так и не обменялся ключами: " + last)
}

// VerifyExit -- критерий успеха. Одного рукопожатия недостаточно: оно бывает
// и когда трафик не ходит, поэтому спрашиваем адрес выхода через туннель и
// напрямую и требуем, чтобы они отличались.
func (d Deps) VerifyExit(ctx context.Context, routerID int64) (string, error) {
	viaRes, err := d.command(ctx, routerID, "check_via_tunnel", map[string]any{})
	if err != nil {
		return "", fmt.Errorf("адрес выхода через новый VPN-туннель проверить не вышло: %w", err)
	}
	directRes, err := d.command(ctx, routerID, "check_direct", map[string]any{})
	if err != nil {
		return "", fmt.Errorf("адрес выхода напрямую проверить не вышло: %w", err)
	}
	via := exitIP(viaRes.Output)
	direct := exitIP(directRes.Output)
	if via == "" {
		return "", errors.New("через новый VPN-туннель адрес выхода не определился")
	}
	if direct != "" && via == direct {
		return "", fmt.Errorf("снаружи виден тот же адрес, что и напрямую (%s): трафик в обход не пошёл", via)
	}
	return fmt.Sprintf("через VPN-туннель %s, напрямую %s", via, orUnknown(direct)), nil
}
