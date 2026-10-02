package backup

import (
	"bufio"
	"bytes"
	"crypto/cipher"
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

// Формат v2 -- потоковый: архив шифруется и расшифровывается блоками, память
// не зависит от размера архива. Раскладка файла:
//
//	"WGMONBACKUP2\n"                      13 байт, магия
//	uint32 BE                             длина заголовка H (1..1 МиБ)
//	H байт JSON                           streamHeader: KDF, соль, префикс nonce, размер блока
//	кадр, кадр, …, последний кадр
//
// Кадр:
//
//	1 байт                                0x00 -- блок не последний, 0x01 -- последний
//	uint32 BE                             длина шифртекста L (вместе с 16 байтами метки)
//	L байт                                XChaCha20-Poly1305(блок открытого текста)
//
// Ключ: Argon2id(парольная фраза, соль 16 байт, параметры из заголовка) --
// та же функция и те же параметры, что у v1. Nonce блока (24 байта) =
// случайный префикс 16 байт из заголовка || uint64 BE номер блока с нуля.
// AAD блока = байты заголовка JSON || байт признака (0x00/0x01).
//
// Что это даёт: перестановка, пропуск и повтор блоков ломают номер в nonce;
// обрезанный файл не имеет блока с признаком «последний», а признак подделать
// нельзя -- он в AAD; хвост после последнего блока -- ошибка; правка
// заголовка ломает первый же блок. Каждый не последний блок несёт ровно
// chunk_size байт открытого текста; последний -- от 0 до chunk_size.
//
// Открытый текст не последних блоков отдаётся читателю до того, как проверен
// конец потока: принимающий обязан считать ошибку чтения провалом всего
// архива, а не «частичным успехом».

var streamMagic = []byte("WGMONBACKUP2\n")

const (
	// StreamChunkSize -- размер блока открытого текста при записи.
	StreamChunkSize = 1 << 20

	streamNoncePrefixSize = 16
	streamFrameHeaderSize = 5
	streamFrameMore       = byte(0x00)
	streamFrameFinal      = byte(0x01)
	maxStreamChunkSize    = 4 << 20
	maxStreamHeaderSize   = 1 << 20
	// Старый формат v1 читается в память целиком; выше этого размера -- отказ.
	maxV1BlobSize = 1 << 30
)

type streamHeader struct {
	Version     int    `json:"v"`
	KDF         string `json:"kdf"`
	AEAD        string `json:"aead"`
	Salt        string `json:"salt"`
	NoncePrefix string `json:"nonce_prefix"`
	Time        uint32 `json:"time"`
	MemoryKiB   uint32 `json:"memory_kib"`
	Threads     uint8  `json:"threads"`
	ChunkSize   uint32 `json:"chunk_size"`
}

type encryptWriter struct {
	w      io.Writer
	aead   cipher.AEAD
	nonce  []byte // префикс + счётчик
	aad    []byte // заголовок + байт признака
	plain  []byte // накопленный блок, len <= chunk
	frame  []byte // буфер кадра, переиспользуется
	chunk  int
	count  uint64
	closed bool
	err    error
}

// NewEncryptWriter пишет в w архив формата v2. Заголовок уходит в w сразу;
// данные -- блоками по StreamChunkSize. Close обязателен: он пишет последний
// блок, без которого архив считается обрезанным. Сам w Close не закрывает.
func NewEncryptWriter(w io.Writer, passphrase []byte, params Params) (io.WriteCloser, error) {
	if len(passphrase) == 0 {
		return nil, errors.New("passphrase is required")
	}
	params = normalizeParams(params)
	if err := validateDecryptParams(params); err != nil {
		return nil, err
	}
	salt := make([]byte, encryptedBackupSaltSize)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, fmt.Errorf("rand salt: %w", err)
	}
	nonce := make([]byte, chacha20poly1305.NonceSizeX)
	if _, err := io.ReadFull(rand.Reader, nonce[:streamNoncePrefixSize]); err != nil {
		return nil, fmt.Errorf("rand nonce prefix: %w", err)
	}
	header, err := json.Marshal(streamHeader{
		Version:     2,
		KDF:         "argon2id",
		AEAD:        "xchacha20poly1305",
		Salt:        base64.RawStdEncoding.EncodeToString(salt),
		NoncePrefix: base64.RawStdEncoding.EncodeToString(nonce[:streamNoncePrefixSize]),
		Time:        params.Time,
		MemoryKiB:   params.MemoryKiB,
		Threads:     params.Threads,
		ChunkSize:   StreamChunkSize,
	})
	if err != nil {
		return nil, err
	}
	key := argon2.IDKey(passphrase, salt, params.Time, params.MemoryKiB, params.Threads, chacha20poly1305.KeySize)
	aead, err := chacha20poly1305.NewX(key)
	clear(key)
	if err != nil {
		return nil, err
	}
	prelude := make([]byte, 0, len(streamMagic)+4+len(header))
	prelude = append(prelude, streamMagic...)
	prelude = binary.BigEndian.AppendUint32(prelude, uint32(len(header))) // #nosec G115 -- заголовок -- сотни байт.
	prelude = append(prelude, header...)
	if _, err := w.Write(prelude); err != nil {
		return nil, err
	}
	aad := make([]byte, len(header)+1)
	copy(aad, header)
	return &encryptWriter{
		w:     w,
		aead:  aead,
		nonce: nonce,
		aad:   aad,
		plain: make([]byte, 0, StreamChunkSize),
		frame: make([]byte, 0, streamFrameHeaderSize+StreamChunkSize+aead.Overhead()),
		chunk: StreamChunkSize,
	}, nil
}

func (e *encryptWriter) Write(p []byte) (int, error) {
	if e.closed {
		return 0, errors.New("write to closed backup encryptor")
	}
	if e.err != nil {
		return 0, e.err
	}
	written := 0
	for len(p) > 0 {
		// Полный блок уходит, только когда за ним точно есть ещё данные:
		// иначе не узнать, последний ли он.
		if len(e.plain) == e.chunk {
			if err := e.seal(streamFrameMore); err != nil {
				return written, err
			}
		}
		n := copy(e.plain[len(e.plain):e.chunk], p)
		e.plain = e.plain[:len(e.plain)+n]
		p = p[n:]
		written += n
	}
	return written, nil
}

func (e *encryptWriter) Close() error {
	if e.closed {
		return e.err
	}
	e.closed = true
	if e.err != nil {
		return e.err
	}
	return e.seal(streamFrameFinal)
}

func (e *encryptWriter) seal(flag byte) error {
	binary.BigEndian.PutUint64(e.nonce[streamNoncePrefixSize:], e.count)
	e.aad[len(e.aad)-1] = flag
	frame := e.frame[:streamFrameHeaderSize]
	frame[0] = flag
	binary.BigEndian.PutUint32(frame[1:], uint32(len(e.plain)+e.aead.Overhead())) // #nosec G115 -- блок не больше StreamChunkSize.
	frame = e.aead.Seal(frame, e.nonce, e.plain, e.aad)
	e.plain = e.plain[:0]
	e.count++
	if _, err := e.w.Write(frame); err != nil {
		e.err = err
		return err
	}
	return nil
}

type decryptReader struct {
	r       *bufio.Reader
	aead    cipher.AEAD
	nonce   []byte
	aad     []byte
	ct      []byte // буфер шифртекста, переиспользуется
	plain   []byte // буфер открытого текста, переиспользуется
	pending []byte // непрочитанный остаток текущего блока
	chunk   int
	count   uint64
	final   bool // последний блок расшифрован
	done    bool // конец потока проверен
	err     error
}

// NewDecryptReader читает архив обоих форматов и сам их различает.
//
// v2 расшифровывается потоком; парольная фраза и заголовок проверяются сразу
// (первый блок расшифровывается до возврата). v1 потока не знает: такой архив
// читается и расшифровывается в памяти целиком -- старые архивы достаточно
// малы (до сотен МБ), новые в этом формате не пишутся.
//
// Ошибка любого Read означает, что архив негоден целиком, даже если часть
// данных уже отдана.
func NewDecryptReader(r io.Reader, passphrase []byte) (io.Reader, error) {
	if len(passphrase) == 0 {
		return nil, errors.New("passphrase is required")
	}
	br := bufio.NewReaderSize(r, 64<<10)
	magic := make([]byte, len(streamMagic))
	if _, err := io.ReadFull(br, magic); err != nil {
		return nil, errors.New("not an encrypted wg-monitor backup")
	}
	switch {
	case bytes.Equal(magic, encryptedMagic):
		rest, err := io.ReadAll(io.LimitReader(br, maxV1BlobSize+1))
		if err != nil {
			return nil, err
		}
		if len(rest) > maxV1BlobSize {
			return nil, errors.New("encrypted backup of the old format is too large")
		}
		plain, err := Decrypt(append(magic, rest...), passphrase)
		if err != nil {
			return nil, err
		}
		return bytes.NewReader(plain), nil
	case !bytes.Equal(magic, streamMagic):
		return nil, errors.New("not an encrypted wg-monitor backup")
	}

	var lenBuf [4]byte
	if _, err := io.ReadFull(br, lenBuf[:]); err != nil {
		return nil, truncated("header length")
	}
	headerLen := binary.BigEndian.Uint32(lenBuf[:])
	if headerLen == 0 || headerLen > maxStreamHeaderSize {
		return nil, errors.New("invalid encrypted backup header")
	}
	header := make([]byte, headerLen)
	if _, err := io.ReadFull(br, header); err != nil {
		return nil, truncated("header")
	}
	var h streamHeader
	if err := json.Unmarshal(header, &h); err != nil {
		return nil, fmt.Errorf("parse encrypted header: %w", err)
	}
	if h.Version != 2 || h.KDF != "argon2id" || h.AEAD != "xchacha20poly1305" {
		return nil, fmt.Errorf("unsupported encrypted backup format v=%d kdf=%q aead=%q", h.Version, h.KDF, h.AEAD)
	}
	params := Params{Time: h.Time, MemoryKiB: h.MemoryKiB, Threads: h.Threads}
	if err := validateDecryptParams(params); err != nil {
		return nil, err
	}
	if h.ChunkSize == 0 || h.ChunkSize > maxStreamChunkSize {
		return nil, fmt.Errorf("invalid block size: got %d want 1..%d", h.ChunkSize, maxStreamChunkSize)
	}
	salt, err := base64.RawStdEncoding.DecodeString(h.Salt)
	if err != nil {
		return nil, fmt.Errorf("decode salt: %w", err)
	}
	if len(salt) != encryptedBackupSaltSize {
		return nil, fmt.Errorf("invalid salt size: got %d want %d", len(salt), encryptedBackupSaltSize)
	}
	prefix, err := base64.RawStdEncoding.DecodeString(h.NoncePrefix)
	if err != nil {
		return nil, fmt.Errorf("decode nonce prefix: %w", err)
	}
	if len(prefix) != streamNoncePrefixSize {
		return nil, errors.New("invalid nonce prefix size")
	}
	key := argon2.IDKey(passphrase, salt, params.Time, params.MemoryKiB, params.Threads, chacha20poly1305.KeySize)
	aead, err := chacha20poly1305.NewX(key)
	clear(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, chacha20poly1305.NonceSizeX)
	copy(nonce, prefix)
	aad := make([]byte, len(header)+1)
	copy(aad, header)
	chunk := int(h.ChunkSize)
	d := &decryptReader{
		r:     br,
		aead:  aead,
		nonce: nonce,
		aad:   aad,
		ct:    make([]byte, 0, chunk+aead.Overhead()),
		plain: make([]byte, 0, chunk),
		chunk: chunk,
	}
	// Первый блок есть всегда (пустой архив -- один пустой последний блок):
	// расшифровав его здесь, сразу узнаём про неверный пароль.
	if err := d.next(); err != nil {
		return nil, err
	}
	return d, nil
}

func truncated(what string) error {
	return fmt.Errorf("encrypted backup is truncated (%s): %w", what, io.ErrUnexpectedEOF)
}

// next читает и расшифровывает следующий блок в d.pending.
func (d *decryptReader) next() error {
	var fh [streamFrameHeaderSize]byte
	if _, err := io.ReadFull(d.r, fh[:]); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return truncated("no final block")
		}
		return err
	}
	flag := fh[0]
	if flag != streamFrameMore && flag != streamFrameFinal {
		return errors.New("invalid encrypted backup block marker")
	}
	n := int(binary.BigEndian.Uint32(fh[1:]))
	overhead := d.aead.Overhead()
	if n < overhead || n > d.chunk+overhead || (flag == streamFrameMore && n != d.chunk+overhead) {
		return fmt.Errorf("invalid encrypted backup block length %d", n)
	}
	ct := d.ct[:n]
	if _, err := io.ReadFull(d.r, ct); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return truncated("block")
		}
		return err
	}
	binary.BigEndian.PutUint64(d.nonce[streamNoncePrefixSize:], d.count)
	d.aad[len(d.aad)-1] = flag
	plain, err := d.aead.Open(d.plain[:0], d.nonce, ct, d.aad)
	if err != nil {
		if d.count == 0 {
			return errors.New("decrypt encrypted backup: wrong passphrase or damaged archive")
		}
		return fmt.Errorf("decrypt encrypted backup: block %d is damaged or out of order", d.count)
	}
	d.count++
	d.pending = plain
	d.final = flag == streamFrameFinal
	return nil
}

func (d *decryptReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for {
		if d.err != nil {
			return 0, d.err
		}
		if len(d.pending) > 0 {
			n := copy(p, d.pending)
			d.pending = d.pending[n:]
			return n, nil
		}
		if d.done {
			return 0, io.EOF
		}
		if d.final {
			// После последнего блока в файле не должно быть ничего.
			if _, err := d.r.ReadByte(); err == nil {
				d.err = errors.New("encrypted backup has trailing data after the final block")
			} else if !errors.Is(err, io.EOF) {
				d.err = err
			} else {
				d.done = true
			}
			continue
		}
		if err := d.next(); err != nil {
			d.err = err
		}
	}
}
