package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/anex/wg-monitor/internal/backup"
)

func ImportEncryptedFullBackup(path, passphrase string, force bool) error {
	body, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read encrypted backup: %w", err)
	}
	if !backup.IsEncrypted(body) {
		return fmt.Errorf("%s is not an encrypted wg-monitor backup", path)
	}
	plainTGZ, err := backup.Decrypt(body, []byte(strings.TrimSpace(passphrase)))
	if err != nil {
		return err
	}
	tmpDir, err := os.MkdirTemp("", "wg-monitor-full-restore.")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)
	legacyPath := filepath.Join(tmpDir, "recovery.tgz")
	if err := os.WriteFile(legacyPath, plainTGZ, 0o600); err != nil {
		return err
	}
	recovery, cleanup, err := InspectRestoreBackup(legacyPath)
	if err != nil {
		return err
	}
	defer cleanup()
	fmt.Println(RenderRestoreBackupPreview(recovery))

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

func extractTarMember(gzBody []byte, name string) ([]byte, error) {
	gr, err := gzip.NewReader(bytes.NewReader(gzBody))
	if err != nil {
		return nil, err
	}
	defer gr.Close()
	tr := tar.NewReader(gr)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, os.ErrNotExist
		}
		if err != nil {
			return nil, err
		}
		if filepath.Base(h.Name) != name {
			continue
		}
		return io.ReadAll(tr)
	}
}
