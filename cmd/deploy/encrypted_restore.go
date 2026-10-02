package main

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Jkaotlic/wg-monitor/internal/backup"
)

// ImportEncryptedFullBackup читает шифрованный архив (v1 или потоковый v2;
// полный или малый) и импортирует из него секреты оператора. Архив
// расшифровывается потоком во временный файл, а не в память.
func ImportEncryptedFullBackup(path, passphrase string, force bool) error {
	encrypted, err := isEncryptedBackupFile(path)
	if err != nil {
		return fmt.Errorf("read encrypted backup: %w", err)
	}
	if !encrypted {
		return fmt.Errorf("%s is not an encrypted wg-monitor backup", path)
	}
	tmpDir, err := os.MkdirTemp("", "wg-monitor-full-restore.")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)
	legacyPath := filepath.Join(tmpDir, "recovery.tgz")
	if err := decryptBackupToFile(path, legacyPath, []byte(strings.TrimSpace(passphrase))); err != nil {
		return err
	}
	recovery, cleanup, err := InspectRestoreBackupForImport(legacyPath)
	if err != nil {
		return err
	}
	defer cleanup()
	fmt.Println(RenderRestoreBackupPreview(recovery))

	plainTGZ, err := os.Open(legacyPath)
	if err != nil {
		return err
	}
	defer plainTGZ.Close()
	vault, err := extractTarMember(plainTGZ, "operator-secrets.tgz.enc")
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			PrintWarn("operator secrets vault missing in encrypted backup")
			return nil
		}
		return err
	}
	operatorPlain, err := backup.Decrypt(vault, []byte(strings.TrimSpace(passphrase)))
	if err != nil {
		return fmt.Errorf("decrypt operator secrets vault: %w", err)
	}
	operatorPath := filepath.Join(tmpDir, "operator-secrets.tgz")
	if err := os.WriteFile(operatorPath, operatorPlain, 0o600); err != nil {
		return err
	}
	return ImportSecrets(operatorPath, DefaultStatePath(), force)
}

// decryptBackupToFile расшифровывает архив src в файл dst (0600) потоком.
func decryptBackupToFile(src, dst string, passphrase []byte) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("read encrypted backup: %w", err)
	}
	defer in.Close()
	dec, err := backup.NewDecryptReader(in, passphrase)
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := out.Close(); err == nil {
			err = cerr
		}
	}()
	_, err = io.Copy(out, dec)
	return err
}

func extractTarMember(gzBody io.Reader, name string) ([]byte, error) {
	gr, err := gzip.NewReader(gzBody)
	if err != nil {
		return nil, err
	}
	defer gr.Close()
	tr := tar.NewReader(gr)
	var found []byte
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			if found == nil {
				return nil, os.ErrNotExist
			}
			return found, nil
		}
		if err != nil {
			return nil, err
		}
		memberName := filepath.ToSlash(h.Name)
		//lint:ignore SA1019 accept both old (TypeRegA/NUL) and new (TypeReg/'0') regular-file
		// markers — backup archives may be produced by non-Go tar implementations that still
		// emit the legacy flag; TypeReg alone would silently skip those members.
		if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeRegA {
			continue
		}
		if memberName != name {
			if filepath.Base(memberName) == name {
				return nil, fmt.Errorf("backup archive contains unexpected member path %q", h.Name)
			}
			continue
		}
		if found != nil {
			return nil, fmt.Errorf("backup archive contains duplicate member %q", h.Name)
		}
		found, err = readArchiveMemberLimited(tr, name, maxOperatorVaultBytes)
		if err != nil {
			return nil, err
		}
	}
}
