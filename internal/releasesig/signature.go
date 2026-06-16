package releasesig

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
)

const (
	PublicKeyBase64              = "LG/jAuU0NHT8bP47UZCbSeBkOTOk1vfqEw8+IAjnDrM="
	signatureRequiredFromVersion = "v0.13.0-rc128"
)

var releaseSigningPublicKey = mustDecodePublicKey(PublicKeyBase64)

func VerifyChecksumsSignature(checksums, signature []byte) error {
	if len(signature) != ed25519.SignatureSize {
		return fmt.Errorf("release signature has invalid size %d", len(signature))
	}
	if !ed25519.Verify(releaseSigningPublicKey, checksums, signature) {
		return fmt.Errorf("release signature verification failed")
	}
	return nil
}

func SignChecksumsWithSeed(checksums []byte, seedB64 string) ([]byte, error) {
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(seedB64))
	if err != nil {
		return nil, fmt.Errorf("decode signing seed: %w", err)
	}
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("signing seed has invalid size %d", len(seed))
	}
	priv := ed25519.NewKeyFromSeed(seed)
	return ed25519.Sign(priv, checksums), nil
}

func SignatureRequiredForVersion(version string) bool {
	version = strings.TrimSpace(version)
	if version == "" {
		return true
	}
	if strings.EqualFold(version, "v0.13.0") {
		return false
	}
	if n, ok := rcNumber(version, "v0.13.0-rc"); ok {
		return n >= 128
	}
	if rank, ok := releaseRank(version); ok {
		min, _ := releaseRank(signatureRequiredFromVersion)
		return compareRank(rank, min) >= 0
	}
	return true
}

func mustDecodePublicKey(raw string) ed25519.PublicKey {
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil || len(key) != ed25519.PublicKeySize {
		panic("invalid release signing public key")
	}
	return ed25519.PublicKey(key)
}

func rcNumber(version, prefix string) (int, bool) {
	if !strings.HasPrefix(version, prefix) {
		return 0, false
	}
	return parseReleaseRankNumber(strings.TrimPrefix(version, prefix))
}

func releaseRank(version string) ([4]int, bool) {
	var rank [4]int
	version = strings.TrimPrefix(strings.TrimSpace(version), "v")
	main, rcRaw, hasRC := strings.Cut(version, "-rc")
	parts := strings.Split(main, ".")
	if len(parts) != 3 {
		return rank, false
	}
	for i, p := range parts {
		n, ok := parseReleaseRankNumber(p)
		if !ok {
			return rank, false
		}
		rank[i] = n
	}
	if !hasRC {
		rank[3] = 1 << 30
		return rank, true
	}
	rc, ok := parseReleaseRankNumber(rcRaw)
	if !ok {
		return rank, false
	}
	rank[3] = rc
	return rank, true
}

func parseReleaseRankNumber(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(s)
	return n, err == nil
}

func compareRank(a, b [4]int) int {
	for i := range a {
		if a[i] > b[i] {
			return 1
		}
		if a[i] < b[i] {
			return -1
		}
	}
	return 0
}
