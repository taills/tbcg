package build

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/taills/tbcg/server/internal/content/release"
)

func newPrivateStoreDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "private-store")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatalf("create private store root: %v", err)
	}
	return dir
}

func TestArtifactStoreInstallsOwnerOnlyArtifactsAndReplaysIdentically(t *testing.T) {
	dir := newPrivateStoreDir(t)
	store, err := OpenArtifactStore(dir)
	if err != nil {
		t.Fatalf("open private artifact store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close artifact store: %v", err)
		}
	})
	artifacts := Artifacts{Manifest: []byte(`{"m":1}`), Public: []byte(`{"p":1}`), Private: []byte(`{"s":1}`)}
	stored, err := store.Install("demo", "build-abc", artifacts)
	if err != nil {
		t.Fatalf("install artifacts: %v", err)
	}
	publicHash := sha256.Sum256(artifacts.Public)
	privateHash := sha256.Sum256(artifacts.Private)
	if hex.EncodeToString(stored.PublicSHA256) != hex.EncodeToString(publicHash[:]) || hex.EncodeToString(stored.PrivateSHA256) != hex.EncodeToString(privateHash[:]) {
		t.Fatal("stored artifact hashes do not match installed bytes")
	}
	for name, want := range map[string][]byte{
		"build-abc.manifest.json": artifacts.Manifest,
		"build-abc.public.json":   artifacts.Public,
		"build-abc.private.json":  artifacts.Private,
	} {
		path := filepath.Join(dir, "demo", name)
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatalf("inspect %s: %v", name, err)
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %v, want regular 0600", name, info.Mode())
		}
		got, err := os.ReadFile(path)
		if err != nil || string(got) != string(want) {
			t.Fatalf("%s content mismatch: %v", name, err)
		}
	}
	gameInfo, err := os.Lstat(filepath.Join(dir, "demo"))
	if err != nil || !gameInfo.IsDir() || gameInfo.Mode().Perm() != 0o700 {
		t.Fatalf("Game artifact directory mode = %v, err=%v", gameInfo.Mode(), err)
	}
	if _, err := store.Install("demo", "build-abc", artifacts); err != nil {
		t.Fatalf("identical replay after crash must reuse installed artifacts: %v", err)
	}
	changed := artifacts
	changed.Private = []byte(`{"s":2}`)
	if _, err := store.Install("demo", "build-abc", changed); !errors.Is(err, release.ErrReleaseConflict) {
		t.Fatalf("different bytes for the same Release error = %v, want conflict", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "demo", "build-abc.private.json"))
	if err != nil || string(got) != `{"s":1}` {
		t.Fatal("conflicting install replaced the original private artifact")
	}
}

func TestArtifactStoreRejectsUnsafeRootsAndIdentities(t *testing.T) {
	wide := filepath.Join(t.TempDir(), "wide")
	if err := os.Mkdir(wide, 0o755); err != nil {
		t.Fatalf("create wide root: %v", err)
	}
	if err := os.Chmod(wide, 0o755); err != nil {
		t.Fatalf("chmod wide root: %v", err)
	}
	if _, err := OpenArtifactStore(wide); err == nil {
		t.Fatal("artifact store accepted a group/world accessible root")
	}
	target := newPrivateStoreDir(t)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("create symlink root: %v", err)
	}
	if _, err := OpenArtifactStore(link); err == nil {
		t.Fatal("artifact store accepted a symlinked root")
	}
	if _, err := OpenArtifactStore(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("artifact store accepted a missing root")
	}

	dir := newPrivateStoreDir(t)
	store, err := OpenArtifactStore(dir)
	if err != nil {
		t.Fatalf("open private store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	artifacts := Artifacts{Manifest: []byte("m"), Public: []byte("p"), Private: []byte("s")}
	for _, ids := range [][2]string{{"../demo", "build-a"}, {"demo", "../build"}, {"demo", ".hidden"}, {"", "build-a"}, {"demo", ""}} {
		if _, err := store.Install(ids[0], ids[1], artifacts); err == nil {
			t.Fatalf("Install(%q, %q) accepted an unsafe identity", ids[0], ids[1])
		}
	}
	if _, err := store.Install("demo", "build-a", Artifacts{Public: []byte("p"), Private: []byte("s")}); err == nil {
		t.Fatal("Install accepted an incomplete artifact set")
	}
	if err := os.Symlink(target, filepath.Join(dir, "linked-game")); err != nil {
		t.Fatalf("create symlinked Game directory: %v", err)
	}
	if _, err := store.Install("linked-game", "build-a", artifacts); err == nil {
		t.Fatal("Install followed a symlinked Game directory")
	}
	if err := os.Mkdir(filepath.Join(dir, "open-game"), 0o755); err != nil {
		t.Fatalf("create open Game directory: %v", err)
	}
	if err := os.Chmod(filepath.Join(dir, "open-game"), 0o755); err != nil {
		t.Fatalf("chmod open Game directory: %v", err)
	}
	if _, err := store.Install("open-game", "build-a", artifacts); err == nil {
		t.Fatal("Install accepted a group/world accessible Game directory")
	}
}
