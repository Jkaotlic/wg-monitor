package backup

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"strings"
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

func TestDecryptRejectsExcessiveKDFParamsBeforeDerivation(t *testing.T) {
	h := testEncryptedHeader()
	h.MemoryKiB = 64*1024 + 1

	_, err := Decrypt(encryptedBlobWithHeader(t, h), []byte("pass"))
	if err == nil || !strings.Contains(err.Error(), "memory_kib") {
		t.Fatalf("want memory_kib validation error, got %v", err)
	}

	h = testEncryptedHeader()
	h.Time = 5
	_, err = Decrypt(encryptedBlobWithHeader(t, h), []byte("pass"))
	if err == nil || !strings.Contains(err.Error(), "time") {
		t.Fatalf("want time validation error, got %v", err)
	}
}

func TestDecryptRejectsInvalidSaltSize(t *testing.T) {
	h := testEncryptedHeader()
	h.Salt = base64.RawStdEncoding.EncodeToString([]byte("short"))

	_, err := Decrypt(encryptedBlobWithHeader(t, h), []byte("pass"))
	if err == nil || !strings.Contains(err.Error(), "salt size") {
		t.Fatalf("want salt size validation error, got %v", err)
	}
}

func testEncryptedHeader() encryptedHeader {
	return encryptedHeader{
		KDF:       "argon2id",
		AEAD:      "xchacha20poly1305",
		Salt:      base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 16)),
		Nonce:     base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{2}, 24)),
		Time:      1,
		MemoryKiB: 1024,
		Threads:   1,
	}
}

func encryptedBlobWithHeader(t *testing.T, h encryptedHeader) []byte {
	t.Helper()
	header, err := json.Marshal(h)
	if err != nil {
		t.Fatal(err)
	}
	out := append([]byte{}, encryptedMagic...)
	var n [4]byte
	binary.BigEndian.PutUint32(n[:], uint32(len(header)))
	out = append(out, n[:]...)
	out = append(out, header...)
	return out
}
