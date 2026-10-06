package build

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/taills/tbcg/server/internal/content/release"
)

const (
	privateDirectoryMode = 0o700
	privateArtifactMode  = 0o600
)

// ArtifactStore installs single-item build artifacts below a private, worker-owned directory.
// It is not served over HTTP; activation and public distribution are later phases.
type ArtifactStore struct {
	root *os.Root
}

// StoredArtifacts reports the hashes of the bytes that are durably installed.
type StoredArtifacts struct {
	PublicSHA256  []byte
	PrivateSHA256 []byte
}

// OpenArtifactStore opens dir only if it is a real owner-only directory, never a symlink.
func OpenArtifactStore(dir string) (*ArtifactStore, error) {
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, fmt.Errorf("inspect private artifact store: %w", err)
	}
	if err := requirePrivateDirectory(info); err != nil {
		return nil, fmt.Errorf("private artifact store %q: %w", dir, err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("open private artifact store: %w", err)
	}
	return &ArtifactStore{root: root}, nil
}

// Install writes manifest/public/private artifacts with atomic no-replace semantics.
// Replaying identical bytes after a crash succeeds; different bytes for the same Release conflict.
func (s *ArtifactStore) Install(gameID, releaseID string, artifacts Artifacts) (StoredArtifacts, error) {
	if !gameIDPattern.MatchString(gameID) || !releaseIDPattern.MatchString(releaseID) {
		return StoredArtifacts{}, errors.New("install build artifacts: invalid Game or Release identity")
	}
	if len(artifacts.Manifest) == 0 || len(artifacts.Public) == 0 || len(artifacts.Private) == 0 {
		return StoredArtifacts{}, errors.New("install build artifacts: incomplete artifact set")
	}
	gameRoot, err := s.openGameDirectory(gameID)
	if err != nil {
		return StoredArtifacts{}, err
	}
	installErr := installArtifactSet(gameRoot, releaseID, artifacts)
	if closeErr := gameRoot.Close(); closeErr != nil {
		installErr = errors.Join(installErr, fmt.Errorf("close Game artifact directory: %w", closeErr))
	}
	if installErr != nil {
		return StoredArtifacts{}, installErr
	}
	publicHash := sha256.Sum256(artifacts.Public)
	privateHash := sha256.Sum256(artifacts.Private)
	return StoredArtifacts{PublicSHA256: publicHash[:], PrivateSHA256: privateHash[:]}, nil
}

// Close releases the store root handle.
func (s *ArtifactStore) Close() error {
	if err := s.root.Close(); err != nil {
		return fmt.Errorf("close private artifact store: %w", err)
	}
	return nil
}

func (s *ArtifactStore) openGameDirectory(gameID string) (*os.Root, error) {
	if err := s.root.Mkdir(gameID, privateDirectoryMode); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, fmt.Errorf("create Game artifact directory: %w", err)
	}
	info, err := s.root.Lstat(gameID)
	if err != nil {
		return nil, fmt.Errorf("inspect Game artifact directory: %w", err)
	}
	if err := requirePrivateDirectory(info); err != nil {
		return nil, fmt.Errorf("artifact directory for Game %q: %w", gameID, err)
	}
	gameRoot, err := s.root.OpenRoot(gameID)
	if err != nil {
		return nil, fmt.Errorf("open Game artifact directory: %w", err)
	}
	return gameRoot, nil
}

func installArtifactSet(gameRoot *os.Root, releaseID string, artifacts Artifacts) error {
	// Payloads first, manifest last: a present manifest implies both payloads were installed and synced.
	for _, artifact := range []struct {
		suffix  string
		content []byte
	}{
		{suffix: ".public.json", content: artifacts.Public},
		{suffix: ".private.json", content: artifacts.Private},
		{suffix: ".manifest.json", content: artifacts.Manifest},
	} {
		if err := release.InstallImmutable(gameRoot, releaseID+artifact.suffix, artifact.content, privateArtifactMode); err != nil {
			return fmt.Errorf("install %s%s: %w", releaseID, artifact.suffix, err)
		}
	}
	return nil
}

func requirePrivateDirectory(info fs.FileInfo) error {
	if info.Mode()&fs.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("must be a real directory, not a symlink or file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("mode %v must not grant group or other access", info.Mode().Perm())
	}
	return nil
}

// Read 读取已安装的产物；kind 为 public、private 或 manifest。
func (s *ArtifactStore) Read(gameID, releaseID, kind string) ([]byte, error) {
	if !gameIDPattern.MatchString(gameID) || !releaseIDPattern.MatchString(releaseID) {
		return nil, errors.New("read build artifact: invalid Game or Release identity")
	}
	if kind != "public" && kind != "private" && kind != "manifest" {
		return nil, errors.New("read build artifact: invalid artifact kind")
	}
	gameRoot, err := s.root.OpenRoot(gameID)
	if err != nil {
		return nil, fmt.Errorf("open Game artifact directory: %w", err)
	}
	defer func() { _ = gameRoot.Close() }()
	return gameRoot.ReadFile(releaseID + "." + kind + ".json")
}
