package actions

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// Прод 18.09: 19 из 19 провалов раскатки -- 503 «прокси занят», и агент не
// повторял и на запасной путь не уходил. Теперь он ждёт Retry-After (с
// разбросом, в пределах бюджета) и, если наш бэкенд так и не отдал выпуск,
// качает тот же выпуск с GitHub -- с той же проверкой подписи и sha256.

// Настоящий GitHub в тестах недостижим: любой запасной путь, который тест не
// подменил сам, упирается в закрытый локальный порт, а не уходит в сеть.
func init() {
	SelfUpdateRepoBase = "http://127.0.0.1:9/github-disabled-in-tests"
}

const fallbackTestVersion = "v0.45.0"
const fallbackTestAsset = "wg-monitor-agent-linux-arm64"

// signedRelease -- выпуск, подписанный ключом, которому верит агент в тесте.
type signedRelease struct {
	artifact  []byte
	checksums []byte
	sig       []byte
}

type fallbackFixture struct {
	good     signedRelease
	sleeps   []time.Duration
	sleepsMu sync.Mutex
}

func newFallbackFixture(t *testing.T) *fallbackFixture {
	t.Helper()
	selfUpdateTestHarness(t, "arm64", true)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	oldVerify := selfUpdateVerifyChecksumsSignature
	selfUpdateVerifyChecksumsSignature = func(checksums, signature []byte) error {
		if len(signature) != ed25519.SignatureSize || !ed25519.Verify(pub, checksums, signature) {
			return errors.New("test release signature verification failed")
		}
		return nil
	}
	t.Cleanup(func() { selfUpdateVerifyChecksumsSignature = oldVerify })

	f := &fallbackFixture{good: makeSignedRelease(priv, []byte("real-agent-v0.45.0"))}
	oldSleep := selfUpdateSleep
	selfUpdateSleep = func(ctx context.Context, d time.Duration) error {
		f.sleepsMu.Lock()
		f.sleeps = append(f.sleeps, d)
		f.sleepsMu.Unlock()
		return ctx.Err()
	}
	t.Cleanup(func() { selfUpdateSleep = oldSleep })
	return f
}

func makeSignedRelease(priv ed25519.PrivateKey, artifact []byte) signedRelease {
	sum := sha256.Sum256(artifact)
	checksums := []byte(hex.EncodeToString(sum[:]) + "  " + fallbackTestAsset + "\n")
	return signedRelease{artifact: artifact, checksums: checksums, sig: ed25519.Sign(priv, checksums)}
}

func (f *fallbackFixture) slept() []time.Duration {
	f.sleepsMu.Lock()
	defer f.sleepsMu.Unlock()
	return append([]time.Duration(nil), f.sleeps...)
}

// releaseServer раздаёт rel; busy(n, file) решает, отказать ли n-му запросу 503.
func releaseServer(t *testing.T, rel signedRelease, busy func(n int32, file string) bool) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		file := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		if busy != nil && busy(n, file) {
			w.Header().Set("Retry-After", "12")
			http.Error(w, `{"error":"release proxy busy; retry shortly"}`, http.StatusServiceUnavailable)
			return
		}
		switch r.URL.Path {
		case "/" + fallbackTestVersion + "/checksums.txt":
			_, _ = w.Write(rel.checksums)
		case "/" + fallbackTestVersion + "/checksums.txt.sig":
			_, _ = w.Write(rel.sig)
		case "/" + fallbackTestVersion + "/" + fallbackTestAsset:
			_, _ = w.Write(rel.artifact)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func useGitHubStub(t *testing.T, base string) {
	t.Helper()
	old := SelfUpdateRepoBase
	SelfUpdateRepoBase = base
	t.Cleanup(func() { SelfUpdateRepoBase = old })
}

func assertInstalled(t *testing.T, want []byte) {
	t.Helper()
	got, err := os.ReadFile(selfUpdateBinPath + ".new")
	if err != nil {
		t.Fatalf("бинарь не скачан: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("скачан не тот бинарь: %q", got)
	}
}

func TestSelfUpdate_BackendBusyRetriesThenSucceeds(t *testing.T) {
	f := newFallbackFixture(t)
	gh, ghHits := releaseServer(t, f.good, nil)
	useGitHubStub(t, gh.URL)
	backend, _ := releaseServer(t, f.good, func(n int32, _ string) bool { return n <= 2 })

	out, err := SelfUpdate(context.Background(), fallbackTestVersion, "", false, backend.URL)
	if err != nil {
		t.Fatalf("SelfUpdate: %v", err)
	}
	assertInstalled(t, f.good.artifact)
	if ghHits.Load() != 0 {
		t.Fatal("бэкенд отдал выпуск после повтора -- на GitHub ходить было незачем")
	}
	if !strings.Contains(out, "backend") {
		t.Fatalf("вывод не называет источник: %q", out)
	}
	sl := f.slept()
	if len(sl) != 2 {
		t.Fatalf("пауз %d, ждали 2: %v", len(sl), sl)
	}
	for _, d := range sl {
		if d < 12*time.Second || d > 18*time.Second {
			t.Fatalf("пауза %v вне Retry-After 12 с + разброс до половины", d)
		}
	}
}

func TestSelfUpdate_BackendBusyForeverFallsBackToGitHub(t *testing.T) {
	f := newFallbackFixture(t)
	gh, ghHits := releaseServer(t, f.good, nil)
	useGitHubStub(t, gh.URL)
	backend, backendHits := releaseServer(t, f.good, func(int32, string) bool { return true })

	out, err := SelfUpdate(context.Background(), fallbackTestVersion, "", false, backend.URL)
	if err != nil {
		t.Fatalf("SelfUpdate: %v", err)
	}
	assertInstalled(t, f.good.artifact)
	if ghHits.Load() == 0 {
		t.Fatal("на GitHub не сходили")
	}
	if backendHits.Load() < 2 {
		t.Fatalf("бэкенду не дали второго шанса: %d запросов", backendHits.Load())
	}
	if !strings.Contains(out, "github") {
		t.Fatalf("вывод не называет GitHub источником: %q", out)
	}
	var total time.Duration
	for _, d := range f.slept() {
		total += d
	}
	if total > selfUpdateBusyBudget {
		t.Fatalf("ждали %v -- больше бюджета %v", total, selfUpdateBusyBudget)
	}
}

// Подпись с чужого источника не сходится -- отказ, а не установка.
func TestSelfUpdate_GitHubFallbackWithBadSignatureRefuses(t *testing.T) {
	f := newFallbackFixture(t)
	_, otherPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	forged := makeSignedRelease(otherPriv, []byte("forged-agent"))
	gh, _ := releaseServer(t, forged, nil)
	useGitHubStub(t, gh.URL)
	backend, _ := releaseServer(t, f.good, func(int32, string) bool { return true })

	_, err = SelfUpdate(context.Background(), fallbackTestVersion, "", false, backend.URL)
	if err == nil {
		t.Fatal("выпуск с чужой подписью установлен")
	}
	if !strings.Contains(err.Error(), "signature") {
		t.Fatalf("ошибка не про подпись: %v", err)
	}
	if strings.Contains(err.Error(), wire.SelfUpdateBusyMarker) {
		t.Fatalf("провал подписи выдан за «прокси занят» -- попытка не засчиталась бы: %v", err)
	}
	if _, statErr := os.Stat(selfUpdateBinPath + ".new"); !os.IsNotExist(statErr) {
		t.Fatal("поддельный бинарь остался на диске")
	}
}

// Подменённый бинарь на GitHub при верной подписи сумм -- отказ по sha256.
func TestSelfUpdate_GitHubFallbackWithWrongBinaryRefuses(t *testing.T) {
	f := newFallbackFixture(t)
	tampered := f.good
	tampered.artifact = []byte("tampered-agent")
	gh, _ := releaseServer(t, tampered, nil)
	useGitHubStub(t, gh.URL)
	backend, _ := releaseServer(t, f.good, func(int32, string) bool { return true })

	_, err := SelfUpdate(context.Background(), fallbackTestVersion, "", false, backend.URL)
	if err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("ждали отказ по sha256, получили %v", err)
	}
}

func TestSelfUpdate_BackendNotFoundFallsBackToGitHub(t *testing.T) {
	f := newFallbackFixture(t)
	gh, _ := releaseServer(t, f.good, nil)
	useGitHubStub(t, gh.URL)
	backend := httptest.NewServer(http.NotFoundHandler())
	defer backend.Close()

	out, err := SelfUpdate(context.Background(), fallbackTestVersion, "", false, backend.URL)
	if err != nil {
		t.Fatalf("SelfUpdate: %v", err)
	}
	assertInstalled(t, f.good.artifact)
	if !strings.Contains(out, "github") {
		t.Fatalf("вывод: %q", out)
	}
	if len(f.slept()) != 0 {
		t.Fatal("на 404 ждать нечего")
	}
}

// Бинарь упёрся в занятый слот, суммы уже получены -- повтор только бинаря.
func TestSelfUpdate_BackendBusyOnBinaryRetries(t *testing.T) {
	f := newFallbackFixture(t)
	gh, ghHits := releaseServer(t, f.good, nil)
	useGitHubStub(t, gh.URL)
	var binaryHits atomic.Int32
	backend, _ := releaseServer(t, f.good, func(_ int32, file string) bool {
		return file == fallbackTestAsset && binaryHits.Add(1) == 1
	})

	if _, err := SelfUpdate(context.Background(), fallbackTestVersion, "", false, backend.URL); err != nil {
		t.Fatalf("SelfUpdate: %v", err)
	}
	assertInstalled(t, f.good.artifact)
	if ghHits.Load() != 0 {
		t.Fatal("GitHub не нужен")
	}
}

// Бэкенд занят, GitHub недоступен -- вывод помечен «занято», чтобы бэкенд не
// засчитал попытку.
func TestSelfUpdate_BusyAndGitHubDownMarksBusy(t *testing.T) {
	f := newFallbackFixture(t)
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "bad gateway", http.StatusBadGateway)
	}))
	defer gh.Close()
	useGitHubStub(t, gh.URL)
	backend, _ := releaseServer(t, f.good, func(int32, string) bool { return true })

	_, err := SelfUpdate(context.Background(), fallbackTestVersion, "", false, backend.URL)
	if err == nil {
		t.Fatal("ждали ошибку")
	}
	if !strings.HasPrefix(err.Error(), wire.SelfUpdateBusyMarker) {
		t.Fatalf("нет маркера занятости в начале: %v", err)
	}
	if !strings.Contains(err.Error(), "HTTP 503") || !strings.Contains(err.Error(), "github") {
		t.Fatalf("вывод обязан назвать обе причины: %v", err)
	}
}

// Бюджет ожидания не выходит за срок действия: запас на скачивание с GitHub
// остаётся. До срока меньше, чем запас плюс Retry-After, -- не ждём вовсе.
func TestSelfUpdate_BusyRetryRespectsDeadline(t *testing.T) {
	f := newFallbackFixture(t)
	gh, _ := releaseServer(t, f.good, nil)
	useGitHubStub(t, gh.URL)
	backend, _ := releaseServer(t, f.good, func(int32, string) bool { return true })

	ctx, cancel := context.WithTimeout(context.Background(), selfUpdateFallbackReserve+5*time.Second)
	defer cancel()
	out, err := SelfUpdate(ctx, fallbackTestVersion, "", false, backend.URL)
	if err != nil {
		t.Fatalf("SelfUpdate: %v", err)
	}
	if len(f.slept()) != 0 {
		t.Fatalf("ждали при сроке впритык: %v", f.slept())
	}
	if !strings.Contains(out, "github") {
		t.Fatalf("вывод: %q", out)
	}
}

func TestParseRetryAfter(t *testing.T) {
	cases := map[string]time.Duration{
		"":     selfUpdateDefaultRetryAfter,
		"junk": selfUpdateDefaultRetryAfter,
		"0":    time.Second,
		"25":   25 * time.Second,
		"9999": selfUpdateMaxRetryAfter,
	}
	for in, want := range cases {
		if got := parseSelfUpdateRetryAfter(in); got != want {
			t.Errorf("parseSelfUpdateRetryAfter(%q)=%v, ждали %v", in, got, want)
		}
	}
}
