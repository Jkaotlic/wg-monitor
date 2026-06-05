package backup

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"
)

var encryptedMagic = []byte("WGMONBACKUP1\n")

type Params struct {
	Time      uint32 `json:"time"`
	MemoryKiB uint32 `json:"memory_kib"`
	Threads   uint8  `json:"threads"`
}

type encryptedHeader struct {
	KDF       string `json:"kdf"`
	AEAD      string `json:"aead"`
	Salt      string `json:"salt"`
	Nonce     string `json:"nonce"`
	Time      uint32 `json:"time"`
	MemoryKiB uint32 `json:"memory_kib"`
	Threads   uint8  `json:"threads"`
}

func DefaultParams() Params {
	return Params{Time: 1, MemoryKiB: 64 * 1024, Threads: 1}
}

func TestParams() Params {
	return Params{Time: 1, MemoryKiB: 1024, Threads: 1}
}

func Encrypt(plain, passphrase []byte, params Params) ([]byte, error) {
	if len(passphrase) == 0 {
		return nil, errors.New("passphrase is required")
	}
	params = normalizeParams(params)
	salt := make([]byte, 16)
	nonce := make([]byte, chacha20poly1305.NonceSizeX)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, fmt.Errorf("rand salt: %w", err)
	}
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("rand nonce: %w", err)
	}
	key := argon2.IDKey(passphrase, salt, params.Time, params.MemoryKiB, params.Threads, chacha20poly1305.KeySize)
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	h := encryptedHeader{
		KDF:       "argon2id",
		AEAD:      "xchacha20poly1305",
		Salt:      base64.RawStdEncoding.EncodeToString(salt),
		Nonce:     base64.RawStdEncoding.EncodeToString(nonce),
		Time:      params.Time,
		MemoryKiB: params.MemoryKiB,
		Threads:   params.Threads,
	}
	header, err := json.Marshal(h)
	if err != nil {
		return nil, err
	}
	if len(header) > 1<<20 {
		return nil, errors.New("encrypted header too large")
	}
	out := make([]byte, 0, len(encryptedMagic)+4+len(header)+len(plain)+aead.Overhead())
	out = append(out, encryptedMagic...)
	var n [4]byte
	binary.BigEndian.PutUint32(n[:], uint32(len(header)))
	out = append(out, n[:]...)
	out = append(out, header...)
	out = aead.Seal(out, nonce, plain, header)
	return out, nil
}

func Decrypt(blob, passphrase []byte) ([]byte, error) {
	if len(passphrase) == 0 {
		return nil, errors.New("passphrase is required")
	}
	if len(blob) < len(encryptedMagic)+4 || string(blob[:len(encryptedMagic)]) != string(encryptedMagic) {
		return nil, errors.New("not an encrypted wg-monitor backup")
	}
	pos := len(encryptedMagic)
	headerLen := int(binary.BigEndian.Uint32(blob[pos : pos+4]))
	pos += 4
	if headerLen <= 0 || headerLen > 1<<20 || len(blob) < pos+headerLen {
		return nil, errors.New("invalid encrypted backup header")
	}
	headerBytes := blob[pos : pos+headerLen]
	pos += headerLen
	var h encryptedHeader
	if err := json.Unmarshal(headerBytes, &h); err != nil {
		return nil, fmt.Errorf("parse encrypted header: %w", err)
	}
	if h.KDF != "argon2id" || h.AEAD != "xchacha20poly1305" {
		return nil, fmt.Errorf("unsupported encrypted backup format kdf=%q aead=%q", h.KDF, h.AEAD)
	}
	salt, err := base64.RawStdEncoding.DecodeString(h.Salt)
	if err != nil {
		return nil, fmt.Errorf("decode salt: %w", err)
	}
	nonce, err := base64.RawStdEncoding.DecodeString(h.Nonce)
	if err != nil {
		return nil, fmt.Errorf("decode nonce: %w", err)
	}
	if len(nonce) != chacha20poly1305.NonceSizeX {
		return nil, errors.New("invalid nonce size")
	}
	params := normalizeParams(Params{Time: h.Time, MemoryKiB: h.MemoryKiB, Threads: h.Threads})
	key := argon2.IDKey(passphrase, salt, params.Time, params.MemoryKiB, params.Threads, chacha20poly1305.KeySize)
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	plain, err := aead.Open(nil, nonce, blob[pos:], headerBytes)
	if err != nil {
		return nil, fmt.Errorf("decrypt encrypted backup: %w", err)
	}
	return plain, nil
}

func IsEncrypted(blob []byte) bool {
	return len(blob) >= len(encryptedMagic) && string(blob[:len(encryptedMagic)]) == string(encryptedMagic)
}

func normalizeParams(p Params) Params {
	if p.Time == 0 {
		p.Time = 1
	}
	if p.MemoryKiB == 0 {
		p.MemoryKiB = 64 * 1024
	}
	if p.Threads == 0 {
		p.Threads = 1
	}
	return p
}
