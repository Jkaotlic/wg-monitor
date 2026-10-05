package selfhostedamnezia

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
)

// Отзыв выданного подключения на своём сервере (v0.55, B3): кнопка админа,
// без автоматики. Подключение -- пир в серверном конфиге и запись в таблице
// клиентов; отзыв убирает пира с работающего интерфейса, затем из обоих файлов.

// ErrClientNotFound -- такого подключения на сервере нет (или ключ не вида ключа).
var ErrClientNotFound = errors.New("подключение не найдено")

// Client -- выданное подключение: запись таблицы клиентов сервера.
type Client struct {
	PublicKey string
	Name      string
	Address   string
	// CreatedAt -- когда выдано; ноль, если запись не разобралась.
	CreatedAt time.Time
}

// creationDateLayout -- как appendClient пишет creationDate.
const creationDateLayout = "Mon Jan 02 15:04:05 2006"

var wgKeyRe = regexp.MustCompile(`^[A-Za-z0-9+/]{43}=$`)

type clientRecord struct {
	ClientID string `json:"clientId"`
	UserData struct {
		AllowedIPs   string `json:"allowedIps"`
		ClientName   string `json:"clientName"`
		CreationDate string `json:"creationDate"`
	} `json:"userData"`
}

func parseClients(raw []byte) ([]json.RawMessage, []Client, error) {
	var items []json.RawMessage
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil, nil, nil
	}
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, nil, err
	}
	out := make([]Client, 0, len(items))
	for _, it := range items {
		var rec clientRecord
		if err := json.Unmarshal(it, &rec); err != nil || rec.ClientID == "" {
			out = append(out, Client{})
			continue
		}
		c := Client{PublicKey: rec.ClientID, Name: rec.UserData.ClientName, Address: rec.UserData.AllowedIPs}
		if t, err := time.Parse(creationDateLayout, rec.UserData.CreationDate); err == nil {
			c.CreatedAt = t
		}
		out = append(out, c)
	}
	return items, out, nil
}

// removePeerBlock вырезает из серверного конфига блок [Peer] с этим ключом;
// остальной текст не трогается. found -- блок был.
func removePeerBlock(conf, pub string) (string, bool) {
	lines := strings.Split(conf, "\n")
	var out []string
	found := false
	for i := 0; i < len(lines); {
		if strings.TrimSpace(lines[i]) != "[Peer]" {
			out = append(out, lines[i])
			i++
			continue
		}
		j := i + 1
		for j < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[j]), "[") {
			j++
		}
		match := false
		for _, l := range lines[i+1 : j] {
			if k, v, ok := strings.Cut(l, "="); ok && strings.TrimSpace(k) == "PublicKey" && strings.TrimSpace(v) == pub {
				match = true
			}
		}
		if match {
			found = true
			// Пустая строка-разделитель перед блоком уходит вместе с ним.
			if n := len(out); n > 0 && strings.TrimSpace(out[n-1]) == "" {
				out = out[:n-1]
			}
		} else {
			out = append(out, lines[i:j]...)
		}
		i = j
	}
	return strings.Join(out, "\n"), found
}

// ClientNameBelongsTo -- имя клиента на сервере выдано роутеру с этим именем:
// «wgmon-<ник>-<ГГГГММДД-ЧЧММСС>» (miniappSelfHostedClientName), после
// очистки и обрезки до 32 знаков, как IssueWithRunner. Обрезанное имя узнаётся
// по префиксу: хвост со временем уже не читается.
func ClientNameBelongsTo(clientName, nickname string) bool {
	full := unsafeNameRe.ReplaceAllString("wgmon-"+nickname+"-", "-")
	if len(full) >= 32 {
		return strings.HasPrefix(clientName, full[:32])
	}
	rest, ok := strings.CutPrefix(clientName, full)
	return ok && stampRe.MatchString(rest)
}

var stampRe = regexp.MustCompile(`^\d{8}-\d{6}$`)

// Clients -- выданные подключения инстанса, в порядке таблицы клиентов.
func (s *Service) Clients(ctx context.Context, id string) ([]Client, Instance, error) {
	id = strings.ToLower(strings.TrimSpace(id))
	unlock := s.lockInstance(id)
	defer unlock()
	inst, cfg, err := s.readyInstance(id)
	if err != nil {
		return nil, Instance{}, err
	}
	raw, err := s.newRunner(cfg, s.hostKeyPolicy(inst)).Run(ctx, []string{"cat", cfg.ClientsPath}, nil)
	if errors.Is(err, ErrHostKeyChanged) {
		return nil, Instance{}, &HostKeyChangedError{Label: inst.Label}
	}
	if err != nil {
		return nil, Instance{}, err
	}
	_, clients, err := parseClients(raw)
	if err != nil {
		return nil, Instance{}, err
	}
	out := clients[:0]
	for _, c := range clients {
		if c.PublicKey != "" {
			out = append(out, c)
		}
	}
	return out, inst, nil
}

// Revoke отзывает подключение по ключу: сначала с работающего интерфейса (оно
// перестаёт работать сразу), затем из серверного конфига и таблицы клиентов.
// Сбой после первого шага -- повтор безопасен: «удалить пира» для отсутствующего
// не ошибка. Возвращает запись отозванного подключения.
func (s *Service) Revoke(ctx context.Context, id, publicKey string) (Client, Instance, error) {
	id = strings.ToLower(strings.TrimSpace(id))
	unlock := s.lockInstance(id)
	defer unlock()
	inst, cfg, err := s.readyInstance(id)
	if err != nil {
		return Client{}, Instance{}, err
	}
	publicKey = strings.TrimSpace(publicKey)
	if !wgKeyRe.MatchString(publicKey) {
		return Client{}, Instance{}, ErrClientNotFound
	}
	runner := s.newRunner(cfg, s.hostKeyPolicy(inst))
	fail := func(err error) (Client, Instance, error) {
		if errors.Is(err, ErrHostKeyChanged) {
			return Client{}, Instance{}, &HostKeyChangedError{Label: inst.Label}
		}
		return Client{}, Instance{}, err
	}
	confB, err := runner.Run(ctx, []string{"cat", cfg.ConfigPath}, nil)
	if err != nil {
		return fail(err)
	}
	tableB, err := runner.Run(ctx, []string{"cat", cfg.ClientsPath}, nil)
	if err != nil {
		return fail(err)
	}
	items, clients, err := parseClients(tableB)
	if err != nil {
		return Client{}, Instance{}, err
	}
	nextConf, inConf := removePeerBlock(string(confB), publicKey)
	var entry Client
	inTable := false
	var nextItems []json.RawMessage
	for i, c := range clients {
		if c.PublicKey == publicKey {
			entry, inTable = c, true
			continue
		}
		nextItems = append(nextItems, items[i])
	}
	if !inConf && !inTable {
		return Client{}, Instance{}, ErrClientNotFound
	}
	if entry.PublicKey == "" {
		entry.PublicKey = publicKey
	}
	if _, err := runner.Run(ctx, []string{"wg", "set", cfg.Interface, "peer", publicKey, "remove"}, nil); err != nil {
		return fail(err)
	}
	if inConf {
		if err := writeAtomic(ctx, runner, cfg.ConfigPath, []byte(nextConf)); err != nil {
			return fail(err)
		}
	}
	if inTable {
		if nextItems == nil {
			nextItems = []json.RawMessage{}
		}
		body, err := json.MarshalIndent(nextItems, "", "    ")
		if err != nil {
			return Client{}, Instance{}, err
		}
		if err := writeAtomic(ctx, runner, cfg.ClientsPath, body); err != nil {
			return fail(err)
		}
	}
	return entry, inst, nil
}
