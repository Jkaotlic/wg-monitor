package selfhostedamnezia

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"

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

// HostKeyPolicy -- чего ждать от ключа хоста. Known -- запомненный отпечаток
// («SHA256:…»); пусто -- первый вход, предъявленный ключ запоминает Remember,
// но только после удачного входа по паролю.
//
// Один раннер -- несколько входов (выпуск читает четыре файла, каждый --
// отдельный вход). pinned -- общий для копий политики отпечаток, который
// записал первый удачный вход: следующие входы того же раннера сверяют его
// в колбэке, до пароля, а не доверяют снова.
type HostKeyPolicy struct {
	Known    string
	Remember func(fingerprint string) error
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
		return ErrHostKeyChanged
	case known == "" && c.policy.Remember == nil:
		c.refused = true
		return errNoHostKeyPolicy
	}
	c.seen = fp
	return nil
}

// remember -- после удачного входа: первый ключ записывается.
func (c *hostKeyCheck) remember() error {
	if c.known != "" || c.seen == "" {
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

// TrustNewHostKey -- «Доверять новому ключу»: запомненный отпечаток
// сбрасывается, следующий удачный вход запомнит тот ключ, что предъявит
// сервер. Право и подтверждение именем -- на стороне обработчика.
func (s *Service) TrustNewHostKey(id string) error {
	id = strings.ToLower(strings.TrimSpace(id))
	return s.update(func(st *Store) error {
		for i := range st.Instances {
			if st.Instances[i].ID == id {
				st.Instances[i].SSHHostKey = ""
				return nil
			}
		}
		return ErrInstanceNotFound
	})
}
