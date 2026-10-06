package selfhostedamnezia

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// Ключ хоста своего сервера (v0.55, B2): доверие при первом входе (TOFU).
// Отпечаток запоминается в записи сервера -- в том же файле своих серверов
// (шифр revive.key через sealedfile), открытого файла рядом нет. Совпал --
// вход; не совпал -- отказ до пароля: пароль на чужой ключ не уходит.

// ErrHostKeyChanged -- ключ хоста своего сервера не совпал с запомненным.
var ErrHostKeyChanged = errors.New("ssh host key changed")

// errNoHostKeyPolicy -- отпечатка нет и запомнить его некуда: такой вход был
// бы без проверки ключа, поэтому его нет вовсе.
var errNoHostKeyPolicy = errors.New("no stored host key fingerprint and nowhere to remember one")

// HostKeyChangedError -- смена ключа с именем сервера для человека.
type HostKeyChangedError struct{ Label string }

func (e *HostKeyChangedError) Error() string {
	return fmt.Sprintf("Ключ сервера «%s» изменился — если вы переустанавливали сервер, подтвердите новый ключ в карточке", e.Label)
}

func (e *HostKeyChangedError) Unwrap() error { return ErrHostKeyChanged }

// ErrHostKeyNotPending -- подтверждают не тот отпечаток, что сервер
// предъявил последним (или ожидающего нет вовсе): доверие не меняется.
var ErrHostKeyNotPending = errors.New("host key fingerprint is not the pending one")

// ErrHostKeyNothingPending -- подтверждать нечего: ожидающего ключа нет
// (доверенный уже совпадает с сервером или адрес SSH сменился).
var ErrHostKeyNothingPending = errors.New("no pending host key to confirm")

// HostKeyPolicy -- чего ждать от ключа хоста. Known -- запомненный отпечаток
// («SHA256:…»); пусто -- первый вход, предъявленный ключ запоминает Remember,
// но только после удачного входа по паролю. Refused -- куда записать
// отпечаток, получивший отказ (ожидающий подтверждения, v0.56, C1). Matched --
// вход с доверенным ключом удался: устаревший ожидающий стирается.
//
// Один раннер -- несколько входов (выпуск читает четыре файла, каждый --
// отдельный вход). pinned -- общий для копий политики отпечаток, который
// записал первый удачный вход: следующие входы того же раннера сверяют его
// в колбэке, до пароля, а не доверяют снова.
type HostKeyPolicy struct {
	Known    string
	Remember func(fingerprint string) error
	Refused  func(fingerprint string) error
	Matched  func(fingerprint string) error
	pinned   *pinnedHostKey
}

type pinnedHostKey struct {
	mu sync.Mutex
	fp string
}

// known -- отпечаток, с которым сверяется вход: запомненный этим раннером
// или тот, что был в файле при его создании.
func (p HostKeyPolicy) known() string {
	if p.pinned != nil {
		p.pinned.mu.Lock()
		fp := p.pinned.fp
		p.pinned.mu.Unlock()
		if fp != "" {
			return fp
		}
	}
	return strings.TrimSpace(p.Known)
}

func (p HostKeyPolicy) pin(fp string) {
	if p.pinned == nil {
		return
	}
	p.pinned.mu.Lock()
	p.pinned.fp = fp
	p.pinned.mu.Unlock()
}

// hostKeyCheck -- одна попытка входа: что предъявил сервер и чем кончилось.
type hostKeyCheck struct {
	policy  HostKeyPolicy
	known   string
	seen    string
	offered string // отказанный отпечаток при смене ключа
	changed bool
	refused bool
}

func (c *hostKeyCheck) callback(_ string, _ net.Addr, key ssh.PublicKey) error {
	fp := ssh.FingerprintSHA256(key)
	known := c.policy.known()
	c.known = known
	switch {
	case known != "" && fp != known:
		c.changed = true
		c.offered = fp
		return ErrHostKeyChanged
	case known == "" && c.policy.Remember == nil:
		c.refused = true
		return errNoHostKeyPolicy
	}
	c.seen = fp
	return nil
}

// remember -- после удачного входа: первый ключ записывается; совпал
// доверенный -- устаревший ожидающий стирается (сбой стирания вход не валит:
// ключ проверен, Matched сам пишет в журнал).
func (c *hostKeyCheck) remember() error {
	if c.seen == "" {
		return nil
	}
	if c.known != "" {
		if c.seen == c.known && c.policy.Matched != nil {
			_ = c.policy.Matched(c.seen)
		}
		return nil
	}
	if err := c.policy.Remember(c.seen); err != nil {
		return err
	}
	c.policy.pin(c.seen)
	return nil
}

// hostKeyPolicy -- политика ключа для инстанса из файла.
func (s *Service) hostKeyPolicy(inst Instance) HostKeyPolicy {
	return HostKeyPolicy{
		Known:  inst.SSHHostKey,
		pinned: &pinnedHostKey{},
		Remember: func(fp string) error {
			return s.rememberHostKey(inst.ID, inst.SSHHost, inst.SSHPort, fp)
		},
		// Сбой записи ожидающего не молчит: карточке тогда нечего
		// подтвердить, а входы стоят -- админ должен увидеть причину в журнале.
		Refused: func(fp string) error {
			err := s.rememberPendingHostKey(inst.ID, inst.SSHHost, inst.SSHPort, fp)
			if err != nil {
				s.logger().Error("свой сервер: ожидающий ключ хоста не записан", "instance", inst.ID, "fingerprint", fp, "err", err)
			}
			return err
		},
		Matched: func(fp string) error {
			err := s.clearPendingHostKey(inst.ID, inst.SSHHost, inst.SSHPort, fp)
			if err != nil {
				s.logger().Error("свой сервер: устаревший ожидающий ключ хоста не стёрт", "instance", inst.ID, "err", err)
			}
			return err
		},
	}
}

var errUnchanged = errors.New("unchanged")

// rememberHostKey записывает первый отпечаток. Пока шёл вход, адрес SSH могли
// сменить -- тогда ключ относится к старому адресу и не пишется. Другой вход
// успел запомнить другой ключ -- это смена ключа.
func (s *Service) rememberHostKey(id, host string, port int, fp string) error {
	err := s.update(func(st *Store) error {
		for i := range st.Instances {
			inst := &st.Instances[i]
			if inst.ID != id {
				continue
			}
			if inst.SSHHost != host || inst.SSHPort != port || inst.SSHHostKey == fp {
				return errUnchanged
			}
			if inst.SSHHostKey != "" {
				return ErrHostKeyChanged
			}
			inst.SSHHostKey = fp
			return nil
		}
		return errUnchanged
	})
	if errors.Is(err, errUnchanged) {
		return nil
	}
	return err
}

// rememberPendingHostKey записывает отказанный отпечаток как ожидающий
// подтверждения. Файл пишется, только если ожидающий изменился и адрес с
// портом SSH те же: каждый выпуск, проверка или автопочинка на сменённом
// ключе иначе переписывали бы файл раз за разом. Совпал с доверенным (его
// успели подтвердить, пока шёл вход) -- ожидающим он не становится.
func (s *Service) rememberPendingHostKey(id, host string, port int, fp string) error {
	fp = strings.TrimSpace(fp)
	if fp == "" {
		return nil
	}
	err := s.update(func(st *Store) error {
		for i := range st.Instances {
			inst := &st.Instances[i]
			if inst.ID != id {
				continue
			}
			if inst.SSHHost != host || inst.SSHPort != port || inst.SSHHostKey == "" || inst.SSHHostKey == fp || inst.SSHHostKeyPending == fp {
				return errUnchanged
			}
			inst.SSHHostKeyPending, inst.SSHHostKeyPendingAt = fp, s.now().UTC()
			return nil
		}
		return errUnchanged
	})
	if errors.Is(err, errUnchanged) {
		return nil
	}
	return err
}

// clearPendingHostKey стирает ожидающий после удачного входа с доверенным
// ключом fp: сервер снова предъявляет прежний ключ, подтверждать нечего.
// Ожидающего нет, адрес/порт сменили или доверенный уже другой -- файл не
// пишется.
func (s *Service) clearPendingHostKey(id, host string, port int, fp string) error {
	err := s.update(func(st *Store) error {
		for i := range st.Instances {
			inst := &st.Instances[i]
			if inst.ID != id {
				continue
			}
			if inst.SSHHostKeyPending == "" || inst.SSHHost != host || inst.SSHPort != port || inst.SSHHostKey != fp {
				return errUnchanged
			}
			inst.SSHHostKeyPending, inst.SSHHostKeyPendingAt = "", time.Time{}
			return nil
		}
		return errUnchanged
	})
	if errors.Is(err, errUnchanged) {
		return nil
	}
	return err
}

func (s *Service) logger() *slog.Logger {
	if s.log != nil {
		return s.log
	}
	return slog.Default()
}

// ConfirmHostKey -- «Подтвердить ключ сервера SHA256:…» (v0.56, C1): админ
// подтверждает именно тот отпечаток, что видел в карточке. Он должен совпасть
// с ожидающим -- тогда становится доверенным, ожидающий стирается. Не
// совпал -- ErrHostKeyNotPending, ожидающего нет -- ErrHostKeyNothingPending;
// ничего не меняется. Право и подтверждение именем --
// на стороне обработчика.
func (s *Service) ConfirmHostKey(id, fingerprint string) error {
	id = strings.ToLower(strings.TrimSpace(id))
	fingerprint = strings.TrimSpace(fingerprint)
	return s.update(func(st *Store) error {
		for i := range st.Instances {
			inst := &st.Instances[i]
			if inst.ID != id {
				continue
			}
			if inst.SSHHostKeyPending == "" {
				return ErrHostKeyNothingPending
			}
			if fingerprint != inst.SSHHostKeyPending {
				return ErrHostKeyNotPending
			}
			inst.SSHHostKey = fingerprint
			inst.SSHHostKeyPending, inst.SSHHostKeyPendingAt = "", time.Time{}
			return nil
		}
		return ErrInstanceNotFound
	})
}
