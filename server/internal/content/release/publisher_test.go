package release

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestValidateDefinitionDistinguishesIDAndOtherInvalidFields(t *testing.T) {
	definition := Definition{
		GameID: "game", RealmID: "realm", ReleaseID: "release-1", ReleaseVersion: 1,
		ContentSchemaVersion: 1, InstanceSchemaVersion: 1, RulesetVersion: 1,
		ProtocolVersion: 1, ClientMinBuild: "1", Manifest: json.RawMessage(`{}`),
	}
	if err := validateDefinition(definition); err != nil {
		t.Fatalf("validate valid release definition: %v", err)
	}
	invalidID := definition
	invalidID.ReleaseID = "../escape"
	if err := validateDefinition(invalidID); !errors.Is(err, ErrReleaseIDInvalid) {
		t.Fatalf("invalid release ID error = %v, want ErrReleaseIDInvalid", err)
	}
	invalidVersion := definition
	invalidVersion.ReleaseVersion = 0
	if err := validateDefinition(invalidVersion); !errors.Is(err, ErrInvalidDefinition) {
		t.Fatalf("invalid release version error = %v, want ErrInvalidDefinition", err)
	}
	invalidGameID := definition
	invalidGameID.GameID = "../outside"
	if err := validateDefinition(invalidGameID); !errors.Is(err, ErrInvalidDefinition) {
		t.Fatalf("path-traversing game ID error = %v, want ErrInvalidDefinition", err)
	}
}

func TestWriteImmutablePreservesExistingArtifact(t *testing.T) {
	path := filepath.Join(t.TempDir(), "release.public")
	if err := writeImmutable(path, []byte("original"), 0o600); err != nil {
		t.Fatalf("write first immutable artifact: %v", err)
	}
	if err := writeImmutable(path, []byte("replacement"), 0o600); !errors.Is(err, os.ErrExist) {
		t.Fatalf("write duplicate artifact error = %v, want os.ErrExist", err)
	}
	actual, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read immutable artifact: %v", err)
	}
	if !bytes.Equal(actual, []byte("original")) {
		t.Fatalf("duplicate write changed artifact to %q", actual)
	}
}

func TestWriteImmutableRejectsDirectoryAndKeepsModePrivate(t *testing.T) {
	root := t.TempDir()
	if err := writeImmutable(root, []byte("data"), 0o600); err == nil {
		t.Fatal("writeImmutable accepted a directory as an artifact file")
	}
	path := filepath.Join(root, "private.package")
	if err := writeImmutable(path, []byte("private"), 0o600); err != nil {
		t.Fatalf("write private artifact: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat private artifact: %v", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("private artifact permissions = %04o", info.Mode().Perm())
	}
}

func TestIncompleteArtifactCleanupPreservesPrimaryAndReportsCleanupErrors(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "partial")
	if err := os.WriteFile(path, []byte("partial"), 0o600); err != nil {
		t.Fatalf("create partial artifact: %v", err)
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open partial artifact: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close file before cleanup injection: %v", err)
	}
	cause := errors.New("primary write failure")
	cleanupErr := discardPartialArtifact(file, path, cause)
	if !errors.Is(cleanupErr, cause) {
		t.Fatalf("cleanup error %v does not preserve primary cause", cleanupErr)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("incomplete artifact remains after cleanup: %v", err)
	}
	missingErr := removePartialArtifact(filepath.Join(root, "missing"), cause)
	if !errors.Is(missingErr, cause) {
		t.Fatalf("missing artifact cleanup changed the primary error: %v", missingErr)
	}
}

func TestManifestWithHashesProducesImmutableCanonicalCopy(t *testing.T) {
	original := []byte(`{"capabilities":["safe"]}`)
	encoded, err := manifestWithHashes(original, "public-hash", "private-hash")
	if err != nil {
		t.Fatalf("add release artifact hashes: %v", err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(encoded, &manifest); err != nil {
		t.Fatalf("decode release manifest: %v", err)
	}
	if manifest["public_artifact_sha256"] != "public-hash" || manifest["private_artifact_sha256"] != "private-hash" {
		t.Fatalf("manifest hashes = %+v", manifest)
	}
	if !bytes.Equal(original, []byte(`{"capabilities":["safe"]}`)) {
		t.Fatal("manifest projection mutated its input")
	}
	for _, invalid := range [][]byte{nil, []byte(`null`), []byte(`[]`), []byte(`{`)} {
		if _, err := manifestWithHashes(invalid, "a", "b"); err == nil {
			t.Errorf("manifestWithHashes accepted invalid input %q", invalid)
		}
	}
}
