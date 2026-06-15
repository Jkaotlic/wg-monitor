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
