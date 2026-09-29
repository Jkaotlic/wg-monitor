package awg3panel

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"time"

	pkcs12 "software.sslmate.com/src/go-pkcs12"
)

const maxP12Size = 64 << 10

// ClientCert -- клиентский сертификат оператора, распакованный из .p12 при
// сохранении. Сам файл и его пароль не хранятся.
type ClientCert struct {
	CertPEM  string
	KeyPEM   string
	Subject  string
	NotAfter time.Time
}

func (ClientCert) String() string       { return hiddenValue }
func (ClientCert) GoString() string     { return hiddenValue }
func (ClientCert) LogValue() slog.Value { return slog.StringValue(hiddenValue) }

// ParseP12 разбирает .p12 (AES-256/PBES2 OpenSSL 3 и старый 3DES/RC2) и
// проверяет, что сертификат действует и ключ к нему подходит. Все отказы --
// FieldError с полем формы.
func ParseP12(data []byte, password string, now time.Time) (ClientCert, error) {
	if len(data) == 0 {
		return ClientCert{}, &FieldError{Field: "p12", Reason: "Выберите файл .p12 с клиентским сертификатом"}
	}
	if len(data) > maxP12Size {
		return ClientCert{}, &FieldError{Field: "p12", Reason: "Файл больше 64 КБ — это не сертификат .p12"}
	}
	key, cert, chain, err := pkcs12.DecodeChain(data, password)
	switch {
	case errors.Is(err, pkcs12.ErrIncorrectPassword):
		return ClientCert{}, &FieldError{Field: "p12_password", Reason: "Пароль от файла .p12 не подошёл"}
	case err != nil:
		return ClientCert{}, &FieldError{Field: "p12", Reason: "Файл не читается как .p12 — выгрузите сертификат заново"}
	case cert == nil || key == nil:
		return ClientCert{}, &FieldError{Field: "p12", Reason: "В файле нет сертификата с ключом"}
	}
	if now.After(cert.NotAfter) {
		return ClientCert{}, &FieldError{Field: "p12", Reason: fmt.Sprintf("Сертификат истёк %s — нужен новый", cert.NotAfter.Format("02.01.2006"))}
	}
	if now.Before(cert.NotBefore) {
		return ClientCert{}, &FieldError{Field: "p12", Reason: fmt.Sprintf("Сертификат начнёт действовать %s", cert.NotBefore.Format("02.01.2006 15:04"))}
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return ClientCert{}, &FieldError{Field: "p12", Reason: "Ключ в файле не поддерживается"}
	}
	var certPEM bytes.Buffer
	_ = pem.Encode(&certPEM, &pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
	for _, c := range chain {
		_ = pem.Encode(&certPEM, &pem.Block{Type: "CERTIFICATE", Bytes: c.Raw})
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	if _, err := tls.X509KeyPair(certPEM.Bytes(), keyPEM); err != nil {
		return ClientCert{}, &FieldError{Field: "p12", Reason: "Ключ в файле не подходит к сертификату"}
	}
	subject := cert.Subject.CommonName
	if subject == "" {
		subject = cert.Subject.String()
	}
	return ClientCert{CertPEM: certPEM.String(), KeyPEM: string(keyPEM), Subject: subject, NotAfter: cert.NotAfter}, nil
}
