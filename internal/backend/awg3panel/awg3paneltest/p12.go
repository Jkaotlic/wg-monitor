package awg3paneltest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"time"

	pkcs12 "software.sslmate.com/src/go-pkcs12"
)

// P12 -- файл .p12 с клиентским сертификатом этого CA и самим CA в цепочке.
// legacy -- 3DES (старые выгрузки Windows), иначе AES-256/PBES2 -- как
// OpenSSL 3 по умолчанию.
func (ca *CA) P12(cn string, notBefore, notAfter time.Time, password string, legacy bool) ([]byte, error) {
	cert, key, err := ca.Leaf(cn, true, notBefore, notAfter)
	if err != nil {
		return nil, err
	}
	enc := pkcs12.Modern2023
	if legacy {
		enc = pkcs12.LegacyDES
	}
	return enc.Encode(key, cert, []*x509.Certificate{ca.Cert}, password)
}

// P12Mismatched -- сертификат с чужим ключом: так бывает, когда файл
// склеивают руками из разных выгрузок.
func (ca *CA) P12Mismatched(password string) ([]byte, error) {
	cert, _, err := ca.Leaf("anex", true, time.Now().Add(-time.Hour), time.Now().Add(24*time.Hour))
	if err != nil {
		return nil, err
	}
	other, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	return pkcs12.Modern2023.Encode(other, cert, nil, password)
}
