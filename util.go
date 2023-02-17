package sbctl

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/foxboron/sbctl/fs"
	"github.com/foxboron/sbctl/logging"
	"github.com/spf13/afero"
)

func ChecksumFile(file string) (string, error) {
	hasher := sha256.New()
	s, err := fs.ReadFile(file)
	if err != nil {
		return "", err
	}
	hasher.Write(s)

	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func CreateDirectory(path string) error {
	_, err := fs.Fs.Stat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		// Ignore this error
	case errors.Is(err, os.ErrExist):
		return nil
	case err != nil:
		return err
	}
	if err := fs.Fs.MkdirAll(path, os.ModePerm); err != nil {
		return err
	}
	return nil
}

func ReadOrCreateFile(filePath string) ([]byte, error) {
	// Try to access or create the file itself
	f, err := fs.ReadFile(filePath)
	if err != nil {
		// Errors will mainly happen due to permissions or non-existing file
		if os.IsNotExist(err) {
			// First, guarantee the directory's existence
			// os.MkdirAll simply returns nil if the directory already exists
			fileDir := filepath.Dir(filePath)
			if err = fs.Fs.MkdirAll(fileDir, os.ModePerm); err != nil {
				return nil, err
			}

			file, err := fs.Fs.Create(filePath)
			if err != nil {
				return nil, err
			}
			file.Close()

			// Create zero-length f, which is equivalent to what would be read from empty file
			f = make([]byte, 0)
		} else {
			if os.IsPermission(err) {
				return nil, err
			}
			return nil, err
		}
	}

	return f, nil
}

var EfivarFSFiles = []string{
	"/sys/firmware/efi/efivars/PK-8be4df61-93ca-11d2-aa0d-00e098032b8c",
	"/sys/firmware/efi/efivars/KEK-8be4df61-93ca-11d2-aa0d-00e098032b8c",
	"/sys/firmware/efi/efivars/db-d719b2cb-3d3a-4596-a3bc-dad00e67656f",
}

var ErrImmutable = errors.New("file is immutable")
var ErrNotImmutable = errors.New("file is not immutable")

var Immutable = false

// Check if a given file has the immutable bit set
func IsImmutable(file string) error {
	// We can't actually do the syscall check. Implemented a workaround to test stuff instead
	if _, ok := fs.Fs.(afero.OsFs); ok {
		if Immutable {
			return ErrImmutable
		}
		return ErrNotImmutable
	}
	f, err := os.Open(file)
	// Files in efivarfs might not exist. Ignore them
	if errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	attr, err := GetAttr(f)
	if err != nil {
		return err
	}
	if (attr & FS_IMMUTABLE_FL) != 0 {
		return ErrImmutable
	}
	return ErrNotImmutable
}

// Check if any files in efivarfs has the immutable bit set
func CheckImmutable() error {
	var isImmutable bool
	for _, file := range EfivarFSFiles {
		err := IsImmutable(file)
		if errors.Is(err, ErrImmutable) {
			isImmutable = true
			logging.Warn("File is immutable: %s", file)
		} else if errors.Is(err, ErrNotImmutable) {
			continue
		} else if err != nil {
			return fmt.Errorf("couldn't read file: %s", file)
		}
	}
	if isImmutable {
		return ErrImmutable
	}
	return nil
}

func CheckMSDos(r io.Reader) (bool, error) {
	// We are looking for MS-DOS executables.
	// They contain "MZ" as the two first bytes
	var header [2]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		// File is smaller than 2 bytes
		if errors.Is(err, io.EOF) {
			return false, nil
		} else if errors.Is(err, io.ErrUnexpectedEOF) {
			return false, nil
		}
		return false, err
	}
	if !bytes.Equal(header[:], []byte{0x4d, 0x5a}) {
		return false, nil
	}
	return true, nil
}

var (
	checked = make(map[string]bool)
)

func AddChecked(path string) {
	normalized := strings.Join(strings.Split(path, "/")[2:], "/")
	checked[normalized] = true
}

func InChecked(path string) bool {
	normalized := strings.Join(strings.Split(path, "/")[2:], "/")
	return checked[normalized]
}

func CopyFile(src, dst string) error {
	source, err := fs.Fs.Open(src)
	if err != nil {
		return err
	}
	defer source.Close()

	f, err := fs.Fs.OpenFile(dst, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err = io.Copy(f, source); err != nil {
		return err
	}
	si, err := fs.Fs.Stat(src)
	if err != nil {
		return err
	}
	return fs.Fs.Chmod(dst, si.Mode())
}

// CopyDirectory moves files and creates directories
func CopyDirectory(src, dst string) error {
	return afero.Walk(fs.Fs, src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relPath := strings.TrimPrefix(path, src)
		newPath := filepath.Join(dst, relPath)
		if info.IsDir() {
			if err := fs.Fs.MkdirAll(newPath, info.Mode()); err != nil {
				return err
			}
			return nil
		}
		if err := CopyFile(path, newPath); err != nil {
			return err
		}
		return nil
	})
}
