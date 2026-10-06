package buildworker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	contentassets "example.com/tbcg/server/internal/content/assets"
	contentbuild "example.com/tbcg/server/internal/content/build"
	"example.com/tbcg/server/internal/content/schema"
)

const testWorkerID = "00000000-0000-4000-8000-0000000000a1"

type fakeQueue struct {
	claim      Claim
	hasJob     bool
	finishOK   bool
	finishErr  error
	failOK     bool
	finished   []finishCall
	failures   []failCall
	claimCalls int
}

type finishCall struct {
	manifest, public, private []byte
}

type failCall struct {
	code      string
	retryable bool
	backoff   int
}

func (q *fakeQueue) Claim(context.Context, string, int) (Claim, bool, error) {
	q.claimCalls++
	return q.claim, q.hasJob, nil
}

func (q *fakeQueue) Finish(_ context.Context, jobID, workerID, token string, manifest, public, private []byte) (bool, error) {
	if jobID != q.claim.JobID || workerID != testWorkerID || token != q.claim.ClaimToken {
		return false, errors.New("unexpected finish identity")
	}
	q.finished = append(q.finished, finishCall{manifest: manifest, public: public, private: private})
	return q.finishOK, q.finishErr
}

func (q *fakeQueue) Fail(_ context.Context, _, _, _ string, code string, retryable bool, backoff int) (bool, error) {
	q.failures = append(q.failures, failCall{code: code, retryable: retryable, backoff: backoff})
	return q.failOK, nil
}

func testValidator(t *testing.T) *schema.Validator {
	t.Helper()
	validator, err := schema.New(os.DirFS("../../../../contracts"))
	if err != nil {
		t.Fatalf("compile trusted schema: %v", err)
	}
	return validator
}

func testClaim(t *testing.T, validator *schema.Validator) Claim {
	t.Helper()
	definition, err := os.ReadFile("../../../../tests/fixtures/content/item.valid.json")
	if err != nil {
		t.Fatalf("read item fixture: %v", err)
	}
	definition = bytes.Replace(definition, []byte(`"icon_id": "item/potion-small"`), []byte(`"icon_id": "demo:icon/placeholder"`), 1)
	var decoded any
	if err := json.Unmarshal(definition, &decoded); err != nil {
		t.Fatalf("decode item fixture: %v", err)
	}
	canonical, err := json.Marshal(decoded)
	if err != nil {
		t.Fatalf("canonicalize item fixture: %v", err)
	}
	bodyHash := sha256.Sum256(canonical)
	asset, err := contentassets.ReadIcon(os.DirFS("../../../../games/demo/assets"), "demo:icon/placeholder")
	if err != nil {
		t.Fatalf("read trusted icon: %v", err)
	}
	manifestHash := sha256.Sum256(asset.Frozen().Manifest)
	snapshot, err := json.Marshal(frozenInput{
		Definition: canonical,
		Profile: contentbuild.CompatibilityProfile{
			ProfileVersion: 3, ContentSchemaVersion: 1, InstanceSchemaVersion: 1, RulesetVersion: 1, ProtocolVersion: 1,
			ClientMinBuild: "1.0.0", AssetManifestSHA256: hex.EncodeToString(manifestHash[:]),
		},
		SchemaFingerprint: validator.Fingerprint(),
		BuilderVersion:    contentbuild.BuilderVersion,
		Asset:             asset.Frozen(),
	})
	if err != nil {
		t.Fatalf("encode frozen input: %v", err)
	}
	return Claim{
		JobID: "00000000-0000-4000-8000-0000000000b1", ClaimToken: "00000000-0000-4000-8000-0000000000c1",
		GameID: "demo", ContentID: "demo:item/healing-potion", Revision: 1, BodyHash: hex.EncodeToString(bodyHash[:]),
		ReleaseID: "build-test-1", ReleaseVersion: 7, InputSnapshot: snapshot,
	}
}

func newTestStore(t *testing.T) (*contentbuild.ArtifactStore, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "store")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatalf("create private store: %v", err)
	}
	store, err := contentbuild.OpenArtifactStore(dir)
	if err != nil {
		t.Fatalf("open private store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, dir
}

func newTestWorker(t *testing.T, queue Queue, store ArtifactInstaller, validator *schema.Validator) *Worker {
	t.Helper()
	worker, err := New(testWorkerID, 30, queue, store, validator)
	if err != nil {
		t.Fatalf("create worker: %v", err)
	}
	return worker
}

func TestRunOnceBuildsInstallsAndFinishesDeterministically(t *testing.T) {
	validator := testValidator(t)
	claim := testClaim(t, validator)
	var outputs [][]byte
	for range 2 {
		queue := &fakeQueue{claim: claim, hasJob: true, finishOK: true}
		store, dir := newTestStore(t)
		claimed, err := newTestWorker(t, queue, store, validator).RunOnce(context.Background())
		if err != nil || !claimed {
			t.Fatalf("RunOnce() = %t, %v", claimed, err)
		}
		if len(queue.finished) != 1 || len(queue.failures) != 0 {
			t.Fatalf("finish/fail calls = %d/%d", len(queue.finished), len(queue.failures))
		}
		call := queue.finished[0]
		var manifest struct {
			Scope          string `json:"scope"`
			ReleaseID      string `json:"release_id"`
			ReleaseVersion int    `json:"release_version"`
			PublicSHA256   string `json:"public_artifact_sha256"`
		}
		if err := json.Unmarshal(call.manifest, &manifest); err != nil {
			t.Fatalf("decode finished manifest: %v", err)
		}
		if manifest.Scope != "single_item" || manifest.ReleaseID != claim.ReleaseID || manifest.ReleaseVersion != 7 || manifest.PublicSHA256 != hex.EncodeToString(call.public) {
			t.Fatalf("finished manifest identity = %#v", manifest)
		}
		installed, err := os.ReadFile(filepath.Join(dir, "demo", claim.ReleaseID+".manifest.json"))
		if err != nil || !bytes.Equal(installed, call.manifest) {
			t.Fatalf("installed manifest differs from finished manifest: %v", err)
		}
		public, err := os.ReadFile(filepath.Join(dir, "demo", claim.ReleaseID+".public.json"))
		if err != nil || bytes.Contains(public, []byte(`"server"`)) {
			t.Fatalf("public artifact missing or leaks private fields: %v", err)
		}
		outputs = append(outputs, installed, public)
	}
	if !bytes.Equal(outputs[0], outputs[2]) || !bytes.Equal(outputs[1], outputs[3]) {
		t.Fatal("two independent workers produced different artifacts from the same frozen input")
	}
}

func TestRunOnceClassifiesFailuresWithoutWritingCompletion(t *testing.T) {
	validator := testValidator(t)
	base := testClaim(t, validator)
	var snapshot map[string]json.RawMessage
	if err := json.Unmarshal(base.InputSnapshot, &snapshot); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	mutate := func(key string, value json.RawMessage) Claim {
		changed := make(map[string]json.RawMessage, len(snapshot))
		for k, v := range snapshot {
			changed[k] = v
		}
		changed[key] = value
		encoded, err := json.Marshal(changed)
		if err != nil {
			t.Fatalf("encode mutated snapshot: %v", err)
		}
		claim := base
		claim.InputSnapshot = encoded
		return claim
	}
	tamperedHash := base
	tamperedHash.BodyHash = hex.EncodeToString(bytes.Repeat([]byte{1}, 32))
	for _, test := range []struct {
		name      string
		claim     Claim
		code      string
		retryable bool
	}{
		{name: "malformed snapshot", claim: Claim{JobID: base.JobID, ClaimToken: base.ClaimToken, InputSnapshot: json.RawMessage(`[]`)}, code: codeInvalidInput},
		{name: "body hash drift", claim: tamperedHash, code: codeInvalidInput},
		{name: "older builder", claim: mutate("builder_version", json.RawMessage(`"item-builder-v0"`)), code: codeDeploymentMismatch, retryable: true},
		{name: "schema fingerprint drift", claim: mutate("schema_fingerprint", json.RawMessage(`"`+hex.EncodeToString(bytes.Repeat([]byte{2}, 32))+`"`)), code: codeDeploymentMismatch, retryable: true},
		{name: "forged asset", claim: mutate("asset", json.RawMessage(`{"asset_id":"demo:icon/placeholder","manifest_bytes":"e30=","asset_bytes":"eA=="}`)), code: codeInvalidInput},
	} {
		t.Run(test.name, func(t *testing.T) {
			queue := &fakeQueue{claim: test.claim, hasJob: true, failOK: true}
			store, dir := newTestStore(t)
			if _, err := newTestWorker(t, queue, store, validator).RunOnce(context.Background()); err != nil {
				t.Fatalf("RunOnce() error = %v", err)
			}
			if len(queue.finished) != 0 || len(queue.failures) != 1 || queue.failures[0].code != test.code || queue.failures[0].retryable != test.retryable {
				t.Fatalf("failure classification = %#v, finished=%d", queue.failures, len(queue.finished))
			}
			if entries, _ := os.ReadDir(dir); len(entries) != 0 {
				t.Fatal("failed build left artifacts in the private store")
			}
		})
	}
}

func TestRunOnceRefusesToReplaceConflictingArtifactsAndReportsLostLease(t *testing.T) {
	validator := testValidator(t)
	claim := testClaim(t, validator)
	store, dir := newTestStore(t)
	if err := os.Mkdir(filepath.Join(dir, "demo"), 0o700); err != nil {
		t.Fatalf("create Game directory: %v", err)
	}
	conflicting := filepath.Join(dir, "demo", claim.ReleaseID+".public.json")
	if err := os.WriteFile(conflicting, []byte(`{"other":true}`), 0o600); err != nil {
		t.Fatalf("seed conflicting artifact: %v", err)
	}
	queue := &fakeQueue{claim: claim, hasJob: true, failOK: true}
	if _, err := newTestWorker(t, queue, store, validator).RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() conflict error = %v", err)
	}
	if len(queue.failures) != 1 || queue.failures[0].code != codeArtifactConflict || queue.failures[0].retryable {
		t.Fatalf("conflict classification = %#v", queue.failures)
	}
	if got, _ := os.ReadFile(conflicting); string(got) != `{"other":true}` {
		t.Fatal("conflicting artifact was replaced")
	}

	freshStore, _ := newTestStore(t)
	lost := &fakeQueue{claim: claim, hasJob: true, finishOK: false}
	if _, err := newTestWorker(t, lost, freshStore, validator).RunOnce(context.Background()); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("RunOnce() after lost lease = %v, want ErrLeaseLost", err)
	}
	idle := &fakeQueue{}
	if claimed, err := newTestWorker(t, idle, freshStore, validator).RunOnce(context.Background()); claimed || err != nil {
		t.Fatalf("RunOnce() with empty queue = %t, %v", claimed, err)
	}
	if _, err := New("", 30, idle, freshStore, validator); err == nil {
		t.Fatal("New accepted an empty worker identity")
	}
	if _, err := New(testWorkerID, 1, idle, freshStore, validator); err == nil {
		t.Fatal("New accepted an unbounded lease")
	}
}

func TestRunOnceSeparatesRejectedCompletionFromUnknownOutcome(t *testing.T) {
	validator := testValidator(t)
	claim := testClaim(t, validator)

	rejectedStore, _ := newTestStore(t)
	rejected := &fakeQueue{claim: claim, hasJob: true, failOK: true, finishErr: errors.Join(ErrCompletionRejected, errors.New("manifest shape"))}
	if _, err := newTestWorker(t, rejected, rejectedStore, validator).RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() with rejected completion = %v, want recorded failure", err)
	}
	if len(rejected.failures) != 1 || rejected.failures[0].code != codeCompletionRejected || rejected.failures[0].retryable {
		t.Fatalf("rejected completion classification = %#v", rejected.failures)
	}

	unknownStore, _ := newTestStore(t)
	transport := errors.New("connection reset")
	unknown := &fakeQueue{claim: claim, hasJob: true, failOK: true, finishErr: transport}
	if _, err := newTestWorker(t, unknown, unknownStore, validator).RunOnce(context.Background()); !errors.Is(err, transport) {
		t.Fatalf("RunOnce() with unknown completion = %v, want returned error for same-worker recovery", err)
	}
	if len(unknown.failures) != 0 {
		t.Fatalf("unknown completion outcome was recorded as a failure: %#v", unknown.failures)
	}
}
