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
	"time"
)

// backupBeforeOverwrite копирует path в path.bak.<stamp>, если path
// существует. Использует io.Copy + 0o600. Любая ошибка — warning, но не
// останавливает import: оператор уже подтвердил force=true и важнее
// сохранить намерение чем pessimistic abort'нуть.
func backupBeforeOverwrite(path, stamp string) {
	src, err := os.Open(path)
	if err != nil {
		// Файла нет (первый импорт) — no-op.
		return
	}
	defer src.Close()
	bakPath := path + ".bak." + stamp
	dst, err := os.OpenFile(bakPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		PrintWarn("backup " + path + ": " + err.Error())
		return
	}
	defer dst.Close()
	if _, err := io.Copy(dst, src); err != nil {
		PrintWarn("backup " + path + ": " + err.Error())
		_ = os.Remove(bakPath)
		return
	}
	PrintInfo("backup: " + bakPath)
}

// archiveMember names — kept short and at the archive root so a sysadmin
// can `tar tzf` and immediately see what's inside.
const (
	archiveSecretsName = "secrets.env"
	archiveStateName   = "wizard.toml"

	maxSecretsArchiveMemberBytes = 1 << 20
	maxStateArchiveMemberBytes   = 1 << 20
	maxOperatorVaultBytes        = 4 << 20
)

// ExportSecrets copies the active secrets cache to dst. Includes the
// wizard.toml alongside in a tar.gz archive (single .tgz file containing
// secrets.env and wizard.toml at the root). dst must end in .tgz.
func ExportSecrets(dst string, statePath string) error {
	if !strings.HasSuffix(strings.ToLower(dst), ".tgz") {
		return fmt.Errorf("destination must end in .tgz, got %q", dst)
	}

	secPath := secretsCachePath()
	if secPath == "" {
		return errors.New("secrets cache disabled (WG_NO_SECRET_CACHE=1) — nothing to export")
	}
	secData, err := os.ReadFile(secPath)
	if err != nil {
		return fmt.Errorf("read secrets %s: %w", secPath, err)
	}

	stateData, err := os.ReadFile(statePath)
	if err != nil {
		// wizard.toml is optional — export still useful if only secrets exist.
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("read state %s: %w", statePath, err)
		}
		stateData = nil
	}

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(dst), err)
	}
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create %s: %w", tmp, err)
	}
	gz := gzip.NewWriter(out)
	tw := tar.NewWriter(gz)

	if err := writeTarFile(tw, archiveSecretsName, secData, 0o600); err != nil {
		tw.Close()
		gz.Close()
		out.Close()
		os.Remove(tmp)
		return fmt.Errorf("tar add secrets: %w", err)
	}
	if stateData != nil {
		if err := writeTarFile(tw, archiveStateName, stateData, 0o600); err != nil {
			tw.Close()
			gz.Close()
			out.Close()
			os.Remove(tmp)
			return fmt.Errorf("tar add state: %w", err)
		}
	}
	if err := tw.Close(); err != nil {
		gz.Close()
		out.Close()
		os.Remove(tmp)
		return fmt.Errorf("tar close: %w", err)
	}
	if err := gz.Close(); err != nil {
		out.Close()
		os.Remove(tmp)
		return fmt.Errorf("gzip close: %w", err)
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("close %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename %s -> %s: %w", tmp, dst, err)
	}

	stateMsg := ""
	if stateData != nil {
		stateMsg = ", wizard.toml"
	}
	PrintOK(fmt.Sprintf("экспортировано в %s (secrets.env%s)", dst, stateMsg))
	return nil
}

// ImportSecrets is the inverse: extracts secrets.env into secretsCachePath()
// (creating the cache dir if needed) and wizard.toml into statePath.
// REFUSES to overwrite existing files unless force=true.
func ImportSecrets(src string, statePath string, force bool) error {
	secPath := secretsCachePath()
	if secPath == "" {
		return errors.New("secrets cache disabled (WG_NO_SECRET_CACHE=1) — refusing to import")
	}

	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open %s: %w", src, err)
	}
	defer in.Close()
	gz, err := gzip.NewReader(in)
	if err != nil {
		return fmt.Errorf("gzip %s: %w", src, err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)

	var secData, stateData []byte
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("tar %s: %w", src, err)
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA {
			continue
		}
		name := filepath.ToSlash(hdr.Name)
		if filepath.IsAbs(hdr.Name) || strings.HasPrefix(name, "/") || strings.Contains(name, "..") {
			return fmt.Errorf("archive contains suspect path %q", hdr.Name)
		}
		switch name {
		case archiveSecretsName:
			if secData != nil {
				return fmt.Errorf("archive contains duplicate archive member %q", hdr.Name)
			}
			b, err := readArchiveMemberLimited(tr, name, maxSecretsArchiveMemberBytes)
			if err != nil {
				return fmt.Errorf("read %s in archive: %w", name, err)
			}
			secData = b
		case archiveStateName:
			if stateData != nil {
				return fmt.Errorf("archive contains duplicate archive member %q", hdr.Name)
			}
			b, err := readArchiveMemberLimited(tr, name, maxStateArchiveMemberBytes)
			if err != nil {
				return fmt.Errorf("read %s in archive: %w", name, err)
			}
			stateData = b
		default:
			switch filepath.Base(name) {
			case archiveSecretsName, archiveStateName:
				return fmt.Errorf("archive contains unexpected archive member path %q", hdr.Name)
			}
			// Silently ignore other entries; future-proofing for added members.
		}
	}

	if secData == nil {
		return fmt.Errorf("archive %s contains no %s", src, archiveSecretsName)
	}

	// Refuse-to-clobber gate.
	if !force {
		if _, err := os.Stat(secPath); err == nil {
			return fmt.Errorf("%s already exists; pass force=true to overwrite", secPath)
		}
		if stateData != nil {
			if _, err := os.Stat(statePath); err == nil {
				return fmt.Errorf("%s already exists; pass force=true to overwrite", statePath)
			}
		}
	}

	// Backup существующих файлов с force=true. Дешёвая страховка от
	// случайного импорта legacy-архива поверх current state — оператор
	// видит .bak.<timestamp> рядом и может откатить вручную.
	stamp := time.Now().UTC().Format("20060102T150405Z")
	if force {
		backupBeforeOverwrite(secPath, stamp)
		if stateData != nil {
			backupBeforeOverwrite(statePath, stamp)
		}
	}

	if err := os.MkdirAll(filepath.Dir(secPath), 0o700); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(secPath), err)
	}
	if err := writeFileMode(secPath, secData, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", secPath, err)
	}

	wroteState := false
	if stateData != nil {
		if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", filepath.Dir(statePath), err)
		}
		if err := writeFileMode(statePath, stateData, 0o600); err != nil {
			return fmt.Errorf("write %s: %w", statePath, err)
		}
		wroteState = true
	}

	stateMsg := ""
	if wroteState {
		stateMsg = " + " + statePath
	}
	PrintOK(fmt.Sprintf("импортировано: %s%s", secPath, stateMsg))
	return nil
}

func readArchiveMemberLimited(r io.Reader, name string, maxBytes int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxBytes {
		return nil, fmt.Errorf("archive member %q too large: limit %d bytes", name, maxBytes)
	}
	return body, nil
}

func writeTarFile(tw *tar.Writer, name string, data []byte, mode int64) error {
	hdr := &tar.Header{
		Name: name,
		Mode: mode,
		Size: int64(len(data)),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err := tw.Write(data)
	return err
}

// writeFileMode writes data atomically with the requested mode. Used so the
// extracted secrets file lands at exactly 0600 even when the umask would
// have widened it.
func writeFileMode(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	// Belt-and-braces: enforce mode via Chmod in case OpenFile honoured the
	// umask (POSIX) and dropped some of the bits we asked for.
	_ = os.Chmod(tmp, mode)
	return os.Rename(tmp, path)
}
