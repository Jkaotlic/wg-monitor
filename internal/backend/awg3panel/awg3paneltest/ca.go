// Package awg3paneltest — поддельная awg3-панель для тестов и песочницы.
// Маршруты — те же шаблоны http.ServeMux, что у настоящей панели
// (awg3-panel@b5ee1c7, internal/web/server.go:80-82 и
// internal/web/handlers_mutate.go:106-116), поэтому readonly-сборка отвечает
// ровно как настоящая: POST на путь, где есть только GET, -- 405 от mux, а
// GET …/config -- 404 text/plain. Авторизация -- как web/auth.go: Basic поверх
// mTLS, 5 неудач за 5 минут -- 429 на 15 минут.
package awg3paneltest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"time"
)

// CA -- одноразовый удостоверяющий центр: им подписаны серверный сертификат
// поддельной панели и клиентский «сертификат оператора».
type CA struct {
	Cert *x509.Certificate
	Key  *ecdsa.PrivateKey
	Pool *x509.CertPool
}

func serial() *big.Int {
	n, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	return n
}

func NewCA(cn string) (*CA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return &CA{Cert: cert, Key: key, Pool: pool}, nil
}

// Leaf -- сертификат, подписанный CA: серверный (127.0.0.1, localhost) или
// клиентский (ExtKeyUsageClientAuth).
func (ca *CA) Leaf(cn string, client bool, notBefore, notAfter time.Time) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	eku := x509.ExtKeyUsageServerAuth
	if client {
		eku = x509.ExtKeyUsageClientAuth
	}
	tpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{eku},
	}
	if !client {
		tpl.IPAddresses = []net.IP{net.IPv4(127, 0, 0, 1)}
		tpl.DNSNames = []string{"localhost"}
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, ca.Cert, &key.PublicKey, ca.Key)
	if err != nil {
		return nil, nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, err
	}
	return cert, key, nil
}

// ClientPEM -- клиентский сертификат и ключ в PEM, как их хранит awg3panel.
func (ca *CA) ClientPEM(cn string) (certPEM, keyPEM []byte, err error) {
	cert, key, err := ca.Leaf(cn, true, time.Now().Add(-time.Hour), time.Now().Add(24*time.Hour))
	if err != nil {
		return nil, nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

func (ca *CA) serverCert() (tls.Certificate, error) {
	cert, key, err := ca.Leaf("awg3-panel-test", false, time.Now().Add(-time.Hour), time.Now().Add(365*24*time.Hour))
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{cert.Raw}, PrivateKey: key, Leaf: cert}, nil
}
