package release

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

func writeImmutable(path string, content []byte, mode os.FileMode) (resultErr error) {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, wrapCleanupError("close artifact root", root.Close())) }()
	return InstallImmutable(root, filepath.Base(path), content, mode)
}

// InstallImmutable atomically installs name inside root without replacing an existing file.
// An existing regular, owner-only file with identical bytes is reused; anything else is a conflict.
func InstallImmutable(root *os.Root, name string, content []byte, mode os.FileMode) (resultErr error) {
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name {
		return fmt.Errorf("artifact name %q must be a single path element: %w", name, ErrReleaseIDInvalid)
	}
	temporary := "." + name + "." + rand.Text() + ".tmp"
	file, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	defer func() {
		resultErr = errors.Join(resultErr, wrapCleanupError("remove staged artifact", root.Remove(temporary)))
	}()
	if err := flushArtifact(file, content); err != nil {
		return err
	}
	// 同目录硬链接提供原子 no-replace 安装；目标名称永不暴露半写文件。
	if err := root.Link(temporary, name); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("install immutable artifact: %w", err)
		}
		if err := reuseArtifact(root, name, content); err != nil {
			return err
		}
	}
	if err := root.Remove(temporary); err != nil {
		return fmt.Errorf("remove installed artifact staging link: %w", err)
	}
	directory, err := root.Open(".")
	if err != nil {
		return fmt.Errorf("open artifact directory for sync: %w", err)
	}
	return errors.Join(wrapCleanupError("sync artifact directory", directory.Sync()),
		wrapCleanupError("close artifact directory", directory.Close()))
}

func flushArtifact(file *os.File, content []byte) error {
	if _, err := file.Write(content); err != nil {
		return errors.Join(err, wrapCleanupError("close incomplete artifact", file.Close()))
	}
	if err := file.Sync(); err != nil {
		return errors.Join(err, wrapCleanupError("close unsynced artifact", file.Close()))
	}
	return file.Close()
}

func reuseArtifact(root *os.Root, name string, content []byte) error {
	info, err := root.Lstat(name)
	if err != nil {
		return fmt.Errorf("inspect existing artifact: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() != int64(len(content)) {
		return fmt.Errorf("unsafe or inconsistent artifact %q: %w", name, errors.Join(ErrReleaseConflict, os.ErrExist))
	}
	existing, err := root.ReadFile(name)
	if err != nil {
		return fmt.Errorf("read existing artifact: %w", err)
	}
	if !bytes.Equal(existing, content) {
		return fmt.Errorf("artifact content differs for %q: %w", name, errors.Join(ErrReleaseConflict, os.ErrExist))
	}
	return nil
}
