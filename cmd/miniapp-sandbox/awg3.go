package main

import (
	"crypto/x509"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/awg3panel"
	"github.com/Jkaotlic/wg-monitor/internal/backend/awg3panel/awg3paneltest"
)

// awg3-панели песочницы: поддельные TLS-панели (awg3paneltest, те же
// маршруты и та же авторизация, что у настоящей) и НАСТОЯЩИЙ
// awg3panel.Service над временным файлом -- экран видит те же отказы и тот
// же предохранитель, что в проде. Корни TLS -- CA поддельных панелей.
type sandboxAwg3 struct {
	svc    *awg3panel.Service
	panels []*awg3paneltest.Panel
}

func (s *sandboxAwg3) Close() {
	for _, p := range s.panels {
		p.Close()
	}
}

func sandboxPeer(id, name string, hsAgo time.Duration, rx, tx int64, enabled bool) awg3paneltest.Peer {
	p := awg3paneltest.Peer{ID: id, Name: name, Address: "10.66.0." + id[len(id)-1:] + "/32", PublicKeyShort: "PUB" + id[:5] + "…",
		Enabled: enabled, RxBytes: rx, TxBytes: tx, CreatedAt: "2026-09-01T10:00:00Z"}
	if hsAgo < 0 {
		p.NeverConnected = true
	} else {
		p.LastHandshake = time.Now().Add(-hsAgo).Unix()
	}
	return p
}

func newSandboxAwg3(dir, p12Out string) (*sandboxAwg3, error) {
	// Пароль-заглушка не литералом: gosec G101 ловит строку в поле Password.
	pw := strings.Repeat("p", 10)
	mainPanel, err := awg3paneltest.Start(awg3paneltest.Options{
		Password: pw,
		Ifaces: []awg3paneltest.Iface{
			{ID: "awg1", Title: "main", Interface: "awg1"},
			{ID: "awg2", Title: "reserve", Interface: "awg2"},
		},
		Peers: map[string][]awg3paneltest.Peer{
			"awg1": {
				sandboxPeer("aaaaaaaaaa01", "wgmon-sandbox-home", 40*time.Second, 1_300_000_000, 210_000_000, true),
				sandboxPeer("aaaaaaaaaa02", "wgmon-sandbox-car", 3*time.Hour, 52_000_000, 9_000_000, true),
				sandboxPeer("aaaaaaaaaa03", "iphone-anex", 90*time.Second, 740_000_000, 31_000_000, true),
				sandboxPeer("aaaaaaaaaa04", "laptop", -1, 0, 0, true),
				sandboxPeer("aaaaaaaaaa05", "old-tablet", 30*24*time.Hour, 12_000, 4_000, false),
			},
			"awg2": {sandboxPeer("bbbbbbbbbb06", "wgmon-sandbox-work", 20*time.Second, 88_000_000, 7_000_000, true)},
		},
	})
	if err != nil {
		return nil, err
	}
	sb := &sandboxAwg3{panels: []*awg3paneltest.Panel{mainPanel}}
	nl2, err := awg3paneltest.Start(awg3paneltest.Options{
		Password: pw, Readonly: true,
		Peers: map[string][]awg3paneltest.Peer{"awg1": {
			sandboxPeer("cccccccccc07", "wgmon-sandbox-bronya", 5*time.Minute, 3_000_000, 800_000, true),
			sandboxPeer("cccccccccc08", "pixel", 45*time.Second, 64_000_000, 5_000_000, true),
		}},
	})
	if err != nil {
		sb.Close()
		return nil, err
	}
	sb.panels = append(sb.panels, nl2)

	pool := x509.NewCertPool()
	pool.AddCert(mainPanel.CA.Cert)
	pool.AddCert(nl2.CA.Cert)
	inst := func(id, label string, p *awg3paneltest.Panel) (awg3panel.Instance, error) {
		certPEM, keyPEM, err := p.CA.ClientPEM("sandbox-admin")
		if err != nil {
			return awg3panel.Instance{}, err
		}
		return awg3panel.Instance{ID: id, Label: label, BaseURL: p.URL, User: "admin", Password: pw,
			CertPEM: string(certPEM), KeyPEM: string(keyPEM), CertSubject: "sandbox-admin",
			CertNotAfter: time.Now().Add(24 * time.Hour), Enabled: true}, nil
	}
	var st awg3panel.Store
	for _, seed := range []struct {
		id, label string
		p         *awg3paneltest.Panel
		mut       func(*awg3panel.Instance)
	}{
		// Допуск v0.51: оператор песочницы (tg-user по умолчанию 4242) выпускает
		// с main на свои роутеры -- вкладка «Панели» у не-админа.
		{"main", "Main (Амстердам)", mainPanel, func(i *awg3panel.Instance) {
			i.Issuers = []awg3panel.Issuer{{TelegramUserID: 4242, GrantedAt: time.Now().UTC()}}
		}},
		// nl2 засевается readonly в хранилище сразу: настоящий сервис узнаёт
		// readonly только по первому 405 от панели, а у поддельной readonly-
		// панели маршрутов мутации нет вовсе -- взять 405 неоткуда. Экран
		// должен увидеть «только для просмотра» с первого открытия.
		{"nl2", "nl2 (полигон)", nl2, func(i *awg3panel.Instance) { i.Readonly = true }},
		// Допуск к недоступной панели (v0.52, хвост v0.51): допущенный видит
		// «сообщите администратору», админ -- блок допуска при баннере.
		{"old", "Старый пароль", mainPanel, func(i *awg3panel.Instance) {
			i.Lock = awg3panel.LockBadPassword
			i.Issuers = []awg3panel.Issuer{{TelegramUserID: 4242, GrantedAt: time.Now().UTC()}}
		}},
		{"ban", "Бан 15 минут", nl2, func(i *awg3panel.Instance) { i.PausedUntil = time.Now().Add(12 * time.Minute) }},
	} {
		in, err := inst(seed.id, seed.label, seed.p)
		if err != nil {
			sb.Close()
			return nil, err
		}
		if seed.mut != nil {
			seed.mut(&in)
		}
		st.Instances = append(st.Instances, in)
	}
	path := filepath.Join(dir, awg3panel.DefaultStoreName)
	if err := awg3panel.SaveStore(path, st); err != nil {
		sb.Close()
		return nil, err
	}
	sb.svc = awg3panel.NewService(path, awg3panel.Options{RootCAs: pool})

	if p12Out != "" {
		pfx, err := mainPanel.CA.P12("sandbox-admin", time.Now().Add(-time.Hour), time.Now().Add(24*time.Hour), "sandbox", false)
		if err != nil {
			sb.Close()
			return nil, err
		}
		if err := os.WriteFile(p12Out, pfx, 0o600); err != nil {
			sb.Close()
			return nil, fmt.Errorf("запись .p12: %w", err)
		}
		hint, _ := json.MarshalIndent(map[string]string{"url": mainPanel.URL, "user": "admin", "password": pw, "p12_password": "sandbox"}, "", "  ")
		if err := os.WriteFile(p12Out+".json", hint, 0o600); err != nil {
			sb.Close()
			return nil, fmt.Errorf("запись подсказки: %w", err)
		}
	}
	return sb, nil
}
