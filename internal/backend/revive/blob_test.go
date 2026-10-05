package revive

import (
	"bytes"
	"errors"
	"testing"
)

// SealBlob/OpenBlob -- файлы кабинетов (v0.55, B1) тем же ключом, что пароли
// root, но в своём домене AAD: шифр одного файла не открывается под именем
// другого и не путается с секретом роутера.
func TestBox_BlobRoundTrip(t *testing.T) {
	box, err := NewBox(testKey(t))
	if err != nil {
		t.Fatal(err)
	}
	plain := []byte(`{"version":1,"routers":{"7":"vpn://secret"}}`)
	nonce, ct, err := box.SealBlob("amnezia-premium.json", plain)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ct, []byte("vpn://secret")) {
		t.Fatal("шифр содержит открытый текст")
	}
	got, err := box.OpenBlob("amnezia-premium.json", nonce, ct)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("got %q", got)
	}
}

func TestBox_BlobForeignDomainFails(t *testing.T) {
	box, _ := NewBox(testKey(t))
	nonce, ct, err := box.SealBlob("amnezia-premium.json", []byte("{}"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := box.OpenBlob("hidemyname.json", nonce, ct); !errors.Is(err, ErrBlobUnreadable) {
		t.Fatalf("чужой домен: err = %v", err)
	}
}

// Домен файла и id роутера не пересекаются: шифр секрета роутера 7 не
// открывается как файл «7», и наоборот.
func TestBox_BlobAndRouterDomainsDisjoint(t *testing.T) {
	box, _ := NewBox(testKey(t))
	nonce, ct, err := box.Seal(7, fixtureSecrets())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := box.OpenBlob("7", nonce, ct); err == nil {
		t.Fatal("секрет роутера открылся как файл")
	}
	nonce, ct, err = box.SealBlob("7", []byte(`{"root_password":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := box.Open(7, nonce, ct); err == nil {
		t.Fatal("файл открылся как секрет роутера")
	}
}

func TestBox_BlobWrongKeyFails(t *testing.T) {
	a, _ := NewBox(testKey(t))
	b, _ := NewBox(testKey(t))
	nonce, ct, _ := a.SealBlob("hidemyname.json", []byte("{}"))
	if _, err := b.OpenBlob("hidemyname.json", nonce, ct); !errors.Is(err, ErrBlobUnreadable) {
		t.Fatalf("чужой ключ: err = %v", err)
	}
}
