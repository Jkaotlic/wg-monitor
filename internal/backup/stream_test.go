package backup

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"runtime"
	"strings"
	"testing"
)

var streamPass = []byte("correct horse battery staple")

func streamEncrypt(t *testing.T, plain []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := NewEncryptWriter(&buf, streamPass, TestParams())
	if err != nil {
		t.Fatal(err)
	}
	// Пишем неровными кусками: границы Write не должны совпадать с блоками.
	for rest := plain; len(rest) > 0; {
		n := 700_001
		if n > len(rest) {
			n = len(rest)
		}
		if _, err := w.Write(rest[:n]); err != nil {
			t.Fatal(err)
		}
		rest = rest[n:]
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func streamDecrypt(blob, pass []byte) ([]byte, error) {
	r, err := NewDecryptReader(bytes.NewReader(blob), pass)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(r)
}

func randomBytes(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestStreamRoundTripSizes(t *testing.T) {
	for _, tc := range []struct {
		name string
		size int
	}{
		{"empty", 0},
		{"one byte", 1},
		{"small", 4096},
		{"chunk minus one", StreamChunkSize - 1},
		{"exactly one chunk", StreamChunkSize},
		{"chunk plus one", StreamChunkSize + 1},
		{"exactly three chunks", 3 * StreamChunkSize},
		{"large uneven", 5*StreamChunkSize + 12345},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plain := randomBytes(t, tc.size)
			blob := streamEncrypt(t, plain)
			if !IsEncrypted(blob) {
				t.Fatal("IsEncrypted не узнал формат v2")
			}
			if tc.size >= 64 && bytes.Contains(blob, plain[:64]) {
				t.Fatal("в шифртексте открытый текст")
			}
			got, err := streamDecrypt(blob, streamPass)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, plain) {
				t.Fatalf("расшифровано %d байт, ждали %d", len(got), len(plain))
			}
			// Decrypt (целиком в память) читает тот же формат.
			got, err = Decrypt(blob, streamPass)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, plain) {
				t.Fatal("Decrypt разошёлся с потоковым чтением")
			}
		})
	}
}

func TestStreamWrongPassphrase(t *testing.T) {
	blob := streamEncrypt(t, []byte("secret"))
	if _, err := streamDecrypt(blob, []byte("wrong")); err == nil {
		t.Fatal("неверная парольная фраза расшифровала архив")
	}
	// Неверный пароль виден сразу, до чтения данных.
	if _, err := NewDecryptReader(bytes.NewReader(blob), []byte("wrong")); err == nil {
		t.Fatal("NewDecryptReader должен отказать сразу")
	}
	if _, err := NewDecryptReader(bytes.NewReader(blob), nil); err == nil {
		t.Fatal("пустая парольная фраза принята")
	}
	if _, err := NewEncryptWriter(io.Discard, nil, TestParams()); err == nil {
		t.Fatal("шифрование с пустой парольной фразой принято")
	}
}

// streamLayout разбирает blob на заголовок и кадры, не расшифровывая.
func streamLayout(t *testing.T, blob []byte) (headerEnd int, frames [][2]int) {
	t.Helper()
	pos := len(streamMagic)
	hl := int(binary.BigEndian.Uint32(blob[pos : pos+4]))
	pos += 4 + hl
	headerEnd = pos
	for pos < len(blob) {
		n := int(binary.BigEndian.Uint32(blob[pos+1 : pos+5]))
		frames = append(frames, [2]int{pos, pos + streamFrameHeaderSize + n})
		pos += streamFrameHeaderSize + n
	}
	if pos != len(blob) {
		t.Fatalf("кадры не сходятся с длиной: %d != %d", pos, len(blob))
	}
	return headerEnd, frames
}

func TestStreamTruncationDetected(t *testing.T) {
	plain := randomBytes(t, 2*StreamChunkSize+100)
	blob := streamEncrypt(t, plain)
	headerEnd, frames := streamLayout(t, blob)
	if len(frames) != 3 {
		t.Fatalf("ждали 3 кадра, получили %d", len(frames))
	}
	cuts := map[string]int{
		"пусто":                      0,
		"середина магии":             5,
		"после магии":                len(streamMagic),
		"середина длины заголовка":   len(streamMagic) + 2,
		"середина заголовка":         headerEnd - 10,
		"заголовок без блоков":       headerEnd,
		"середина заголовка кадра":   frames[0][0] + 3,
		"середина первого блока":     frames[0][0] + 1000,
		"после первого блока":        frames[0][1],
		"середина второго блока":     frames[1][0] + 5000,
		"нет последнего блока":       frames[1][1],
		"середина последнего блока":  frames[2][0] + 50,
		"последний блок без байта":   len(blob) - 1,
		"последний блок без метки":   len(blob) - 16,
		"заголовок последнего кадра": frames[2][0] + streamFrameHeaderSize,
	}
	for name, cut := range cuts {
		t.Run(name, func(t *testing.T) {
			got, err := streamDecrypt(blob[:cut], streamPass)
			if err == nil {
				t.Fatalf("усечение до %d байт не замечено (прочитано %d)", cut, len(got))
			}
			if errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("усечение не должно выглядеть как обычный конец: %v", err)
			}
		})
	}
}

func TestStreamSwappedChunksDetected(t *testing.T) {
	plain := randomBytes(t, 3*StreamChunkSize)
	blob := streamEncrypt(t, plain)
	headerEnd, frames := streamLayout(t, blob)
	if len(frames) != 3 {
		t.Fatalf("ждали 3 кадра, получили %d", len(frames))
	}
	swapped := append([]byte{}, blob[:headerEnd]...)
	swapped = append(swapped, blob[frames[1][0]:frames[1][1]]...)
	swapped = append(swapped, blob[frames[0][0]:frames[0][1]]...)
	swapped = append(swapped, blob[frames[2][0]:frames[2][1]]...)
	if _, err := streamDecrypt(swapped, streamPass); err == nil {
		t.Fatal("перестановка блоков не замечена")
	}

	// Выброшенный средний блок.
	dropped := append([]byte{}, blob[:frames[1][0]]...)
	dropped = append(dropped, blob[frames[2][0]:]...)
	if _, err := streamDecrypt(dropped, streamPass); err == nil {
		t.Fatal("пропуск блока не замечен")
	}

	// Повтор блока.
	dup := append([]byte{}, blob[:frames[1][1]]...)
	dup = append(dup, blob[frames[1][0]:]...)
	if _, err := streamDecrypt(dup, streamPass); err == nil {
		t.Fatal("повтор блока не замечен")
	}
}

func TestStreamFinalFlagIsAuthenticated(t *testing.T) {
	plain := randomBytes(t, 2*StreamChunkSize)
	blob := streamEncrypt(t, plain)
	_, frames := streamLayout(t, blob)

	// Первый блок выдан за последний, остальное отрезано: усечение с подделкой метки.
	forged := append([]byte{}, blob[:frames[0][1]]...)
	forged[frames[0][0]] = streamFrameFinal
	if _, err := streamDecrypt(forged, streamPass); err == nil {
		t.Fatal("подделка признака последнего блока не замечена")
	}

	// Последний блок выдан за промежуточный.
	forged = append([]byte{}, blob...)
	forged[frames[len(frames)-1][0]] = streamFrameMore
	if _, err := streamDecrypt(forged, streamPass); err == nil {
		t.Fatal("снятие признака последнего блока не замечено")
	}
}

func TestStreamAppendedGarbageDetected(t *testing.T) {
	for _, size := range []int{0, 100, StreamChunkSize} {
		blob := streamEncrypt(t, randomBytes(t, size))
		for _, tail := range [][]byte{{0}, []byte("garbage after the archive"), blob} {
			if _, err := streamDecrypt(append(append([]byte{}, blob...), tail...), streamPass); err == nil {
				t.Fatalf("хвост из %d байт после архива (%d) не замечен", len(tail), size)
			}
		}
	}
}

func TestStreamBitFlipDetected(t *testing.T) {
	blob := streamEncrypt(t, randomBytes(t, StreamChunkSize+10))
	headerEnd, frames := streamLayout(t, blob)
	for _, pos := range []int{len(streamMagic) + 6, headerEnd - 3, frames[0][0] + 100, frames[1][1] - 1} {
		bad := append([]byte{}, blob...)
		bad[pos] ^= 0x01
		if _, err := streamDecrypt(bad, streamPass); err == nil {
			t.Fatalf("искажение байта %d не замечено", pos)
		}
	}
}

func TestStreamRejectsOversizedFrameAndBadHeader(t *testing.T) {
	blob := streamEncrypt(t, []byte("x"))
	_, frames := streamLayout(t, blob)
	bad := append([]byte{}, blob...)
	binary.BigEndian.PutUint32(bad[frames[0][0]+1:], 1<<30)
	if _, err := streamDecrypt(bad, streamPass); err == nil || !strings.Contains(err.Error(), "block") {
		t.Fatalf("ждали отказ по длине блока, получили %v", err)
	}
	// Заголовок с непомерной длиной не должен приводить к выделению памяти.
	huge := append([]byte{}, streamMagic...)
	huge = append(huge, 0xff, 0xff, 0xff, 0xff)
	if _, err := streamDecrypt(huge, streamPass); err == nil {
		t.Fatal("заголовок на 4 ГБ принят")
	}
}

func TestDecryptReaderReadsV1(t *testing.T) {
	plain := []byte("старый архив одним куском")
	blob, err := Encrypt(plain, streamPass, TestParams())
	if err != nil {
		t.Fatal(err)
	}
	got, err := streamDecrypt(blob, streamPass)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("v1 через потоковое чтение: %q", got)
	}
	if _, err := streamDecrypt(blob, []byte("wrong")); err == nil {
		t.Fatal("v1 с неверным паролем расшифрован")
	}
	if _, err := streamDecrypt([]byte{0x1f, 0x8b, 0x08, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}, streamPass); err == nil {
		t.Fatal("обычный gzip принят за шифрованный архив")
	}
}

func TestEncryptWriterCloseIsIdempotentAndSealsOnce(t *testing.T) {
	var buf bytes.Buffer
	w, err := NewEncryptWriter(&buf, streamPass, TestParams())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	size := buf.Len()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != size {
		t.Fatal("повторный Close дописал данные")
	}
	if _, err := w.Write([]byte("d")); err == nil {
		t.Fatal("Write после Close принят")
	}
}

// zeroReader отдаёт n нулевых байт.
type zeroReader struct{ left int64 }

func (z *zeroReader) Read(p []byte) (int, error) {
	if z.left == 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > z.left {
		p = p[:z.left]
	}
	clear(p)
	z.left -= int64(len(p))
	return len(p), nil
}

// 200 МБ через шифрование и расшифровку: память не растёт с длиной потока.
// Мерим и пик кучи, и суммарные выделения -- второе ловит буфер «на блок».
func TestStreamMemoryIsBounded(t *testing.T) {
	const total = 200 << 20
	const heapLimit = 16 << 20
	const allocLimit = 32 << 20

	pr, pw := io.Pipe()
	encErr := make(chan error, 1)

	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	go func() {
		w, err := NewEncryptWriter(pw, streamPass, TestParams())
		if err != nil {
			pw.CloseWithError(err)
			encErr <- err
			return
		}
		buf := make([]byte, 256<<10)
		_, err = io.CopyBuffer(w, io.LimitReader(&zeroReader{left: total}, total), buf)
		if err == nil {
			err = w.Close()
		}
		pw.CloseWithError(err)
		encErr <- err
	}()

	r, err := NewDecryptReader(pr, streamPass)
	if err != nil {
		t.Fatal(err)
	}
	var (
		got      int64
		peakHeap uint64
		ms       runtime.MemStats
		buf      = make([]byte, 256<<10)
		nextStat = int64(8 << 20)
	)
	for {
		n, err := r.Read(buf)
		for _, b := range buf[:n] {
			if b != 0 {
				t.Fatal("расшифрован не ноль")
			}
		}
		got += int64(n)
		if got >= nextStat {
			nextStat += 8 << 20
			runtime.ReadMemStats(&ms)
			if ms.HeapAlloc > peakHeap {
				peakHeap = ms.HeapAlloc
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := <-encErr; err != nil {
		t.Fatal(err)
	}
	if got != total {
		t.Fatalf("прочитано %d байт, ждали %d", got, total)
	}
	runtime.ReadMemStats(&ms)
	heapGrowth := int64(peakHeap) - int64(before.HeapAlloc)
	allocated := ms.TotalAlloc - before.TotalAlloc
	t.Logf("поток %d МБ: рост пика кучи %.2f МБ, всего выделено %.2f МБ", total>>20,
		float64(heapGrowth)/(1<<20), float64(allocated)/(1<<20))
	if heapGrowth > heapLimit {
		t.Fatalf("пик кучи вырос на %d байт, предел %d", heapGrowth, heapLimit)
	}
	if allocated > allocLimit {
		t.Fatalf("за поток выделено %d байт, предел %d", allocated, allocLimit)
	}
}
