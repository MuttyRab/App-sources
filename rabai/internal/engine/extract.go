package engine

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Extract unpacks an engine zip into root/<hash-prefix> once and returns that
// directory. A ".complete" marker makes interrupted extractions retry.
func Extract(payload []byte, root string) (string, error) {
	sum := sha256.Sum256(payload)
	dir := filepath.Join(root, hex.EncodeToString(sum[:])[:12])
	if _, err := os.Stat(filepath.Join(dir, ".complete")); err == nil {
		return dir, nil
	}
	zr, err := zip.NewReader(bytes.NewReader(payload), int64(len(payload)))
	if err != nil {
		return "", fmt.Errorf("engine archive: %w", err)
	}
	tmp := dir + ".tmp"
	os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return "", err
	}
	for _, f := range zr.File {
		name := filepath.Clean(f.Name)
		if strings.HasPrefix(name, "..") || filepath.IsAbs(name) {
			return "", fmt.Errorf("engine archive: bad path %q", f.Name)
		}
		dst := filepath.Join(tmp, name)
		if f.FileInfo().IsDir() {
			os.MkdirAll(dst, 0o755)
			continue
		}
		os.MkdirAll(filepath.Dir(dst), 0o755)
		if err := writeZipFile(f, dst); err != nil {
			return "", err
		}
	}
	os.WriteFile(filepath.Join(tmp, ".complete"), []byte("ok"), 0o644)
	os.RemoveAll(dir)
	if err := os.Rename(tmp, dir); err != nil {
		return "", err
	}
	// Remove engines left over from older app versions.
	if entries, err := os.ReadDir(root); err == nil {
		for _, e := range entries {
			if p := filepath.Join(root, e.Name()); p != dir && e.IsDir() {
				os.RemoveAll(p)
			}
		}
	}
	return dir, nil
}

func writeZipFile(f *zip.File, dst string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, rc); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
