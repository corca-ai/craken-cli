package craken

import (
	"io"
	"os"
	"path/filepath"
)

// Stage on the destination filesystem so a truncated response never replaces an existing file.
func writeDownload(path string, body io.Reader) error {
	path = filepath.Clean(path)
	file, err := os.CreateTemp(filepath.Dir(path), ".craken-download-*")
	if err != nil {
		return err
	}
	name := file.Name()
	defer func() { _ = file.Close(); _ = os.Remove(name) }()
	if _, err := io.Copy(file, body); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
