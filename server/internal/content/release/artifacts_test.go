package release

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteImmutableRejectsPartialTargetWithoutChangingIt(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "release.public")
	partial := []byte("short")
	if err := os.WriteFile(path, partial, 0o600); err != nil {
		t.Fatalf("create partial target artifact: %v", err)
	}

	err := writeImmutable(path, []byte("complete expected artifact"), 0o600)
	if !errors.Is(err, ErrReleaseConflict) || !errors.Is(err, os.ErrExist) {
		t.Fatalf("write over partial target error = %v, want ErrReleaseConflict and os.ErrExist", err)
	}
	actual, err := os.ReadFile(path)
	if err != nil || string(actual) != string(partial) {
		t.Fatalf("partial target changed to %q, read error %v", actual, err)
	}
	assertDirectoryEntries(t, root, "release.public")
}

func TestWriteImmutableRejectsSymlinkWithoutChangingTargetOrLink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "sentinel")
	if err := os.WriteFile(target, []byte("do not replace"), 0o600); err != nil {
		t.Fatalf("create symlink target: %v", err)
	}
	path := filepath.Join(root, "release.private")
	if err := os.Symlink(target, path); err != nil {
		t.Fatalf("create artifact symlink: %v", err)
	}

	err := writeImmutable(path, []byte("replacement"), 0o600)
	if !errors.Is(err, ErrReleaseConflict) || !errors.Is(err, os.ErrExist) {
		t.Fatalf("write over symlink error = %v, want ErrReleaseConflict and os.ErrExist", err)
	}
	linkTarget, err := os.Readlink(path)
	if err != nil || linkTarget != target {
		t.Fatalf("artifact symlink target = %q, error %v; want %q", linkTarget, err, target)
	}
	actual, err := os.ReadFile(target)
	if err != nil || string(actual) != "do not replace" {
		t.Fatalf("symlink target changed to %q, read error %v", actual, err)
	}
	assertDirectoryEntries(t, root, "release.private")
}

func TestWriteImmutableHardLinkInstallAndRetryLeaveNoStagingFiles(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "release.public")
	content := []byte("complete immutable artifact")
	if err := writeImmutable(path, content, 0o600); err != nil {
		t.Fatalf("install immutable artifact through hard link: %v", err)
	}
	assertDirectoryEntries(t, root, "release.public")

	if err := writeImmutable(path, content, 0o600); err != nil {
		t.Fatalf("reuse identical installed artifact: %v", err)
	}
	actual, err := os.ReadFile(path)
	if err != nil || string(actual) != string(content) {
		t.Fatalf("reused artifact = %q, read error %v", actual, err)
	}
	assertDirectoryEntries(t, root, "release.public")
}

func assertDirectoryEntries(t *testing.T, directory string, want string) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("read artifact directory: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != want {
		names := make([]string, len(entries))
		for index, entry := range entries {
			names[index] = entry.Name()
		}
		t.Fatalf("artifact directory entries = %v, want only %q", names, want)
	}
}
