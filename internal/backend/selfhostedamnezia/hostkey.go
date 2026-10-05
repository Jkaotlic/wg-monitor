package selfhostedamnezia

import (
	"errors"
	"fmt"
	"net"
	"strings"

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
var errNoHostKeyPolicy = errors.New("ssh host key: no stored fingerprint and nowhere to remember one")

// HostKeyChangedError -- смена ключа с именем сервера для человека.
type HostKeyChangedError struct{ Label string }

func (e *HostKeyChangedError) Error() string {
	return fmt.Sprintf("Ключ сервера «%s» изменился — если вы переустанавливали сервер, подтвердите новый ключ в карточке", e.Label)
}

func (e *HostKeyChangedError) Unwrap() error { return ErrHostKeyChanged }

// HostKeyPolicy -- чего ждать от ключа хоста. Known -- запомненный отпечаток
// («SHA256:…»); пусто -- первый вход, предъявленный ключ запоминает Remember,
// но только после удачного входа по паролю.
type HostKeyPolicy struct {
	Known    string
	Remember func(fingerprint string) error
}

// hostKeyCheck -- одна попытка входа: что предъявил сервер и чем кончилось.
type hostKeyCheck struct {
	policy  HostKeyPolicy
	seen    string
	changed bool
	refused bool
}

func (c *hostKeyCheck) callback(_ string, _ net.Addr, key ssh.PublicKey) error {
	fp := ssh.FingerprintSHA256(key)
	known := strings.TrimSpace(c.policy.Known)
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
	if strings.TrimSpace(c.policy.Known) != "" || c.seen == "" {
		return nil
	}
	return c.policy.Remember(c.seen)
}

// hostKeyPolicy -- политика ключа для инстанса из файла.
func (s *Service) hostKeyPolicy(inst Instance) HostKeyPolicy {
	return HostKeyPolicy{
		Known: inst.SSHHostKey,
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
