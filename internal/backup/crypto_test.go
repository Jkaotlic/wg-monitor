package backup

import (
	"bytes"
	"testing"
)

func TestEncryptDecryptRoundTrip(t *testing.T) {
	plain := []byte("state + secrets bundle")
	pass := []byte("correct horse battery staple")

	blob, err := Encrypt(plain, pass, TestParams())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(blob, plain) {
		t.Fatalf("encrypted blob contains plaintext: %q", blob)
	}

	got, err := Decrypt(blob, pass)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("decrypt mismatch: got %q want %q", got, plain)
	}
}

func TestDecryptRejectsWrongPassword(t *testing.T) {
	blob, err := Encrypt([]byte("secret"), []byte("right"), TestParams())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decrypt(blob, []byte("wrong")); err == nil {
		t.Fatal("wrong password decrypted successfully")
	}
}

func TestDecryptRejectsLegacyTarball(t *testing.T) {
	if _, err := Decrypt([]byte{0x1f, 0x8b, 0x08}, []byte("pass")); err == nil {
		t.Fatal("legacy gzip tarball should not be treated as encrypted backup")
	}
}
