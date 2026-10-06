package admin

import (
	"bytes"
	"testing"
)

func TestValidateBuildRequestRequiresExactApprovedIdentity(t *testing.T) {
	valid := BuildRequest{ContentID: "demo:item/potion", Revision: 1, BodyHash: string(bytes.Repeat([]byte{'a'}, 64))}
	if err := validateBuildRequest("demo", valid); err != nil {
		t.Fatalf("validate valid build request: %v", err)
	}

	tests := []struct {
		name  string
		game  string
		input BuildRequest
	}{
		{name: "invalid Game path", game: "../demo", input: valid},
		{name: "empty content identity", game: "demo", input: BuildRequest{Revision: 1, BodyHash: valid.BodyHash}},
		{name: "zero revision", game: "demo", input: BuildRequest{ContentID: valid.ContentID, BodyHash: valid.BodyHash}},
		{name: "uppercase SHA", game: "demo", input: BuildRequest{ContentID: valid.ContentID, Revision: 1, BodyHash: string(bytes.Repeat([]byte{'A'}, 64))}},
		{name: "short SHA", game: "demo", input: BuildRequest{ContentID: valid.ContentID, Revision: 1, BodyHash: "abc"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateBuildRequest(test.game, test.input); err == nil {
				t.Fatal("validateBuildRequest() accepted invalid request")
			}
		})
	}
}

func TestValidateIdempotencyKeyUsesBoundedSafeAlphabet(t *testing.T) {
	for _, key := range []string{"0123456789abcdef", "release-job_2026-10~retry"} {
		if err := validateIdempotencyKey(key); err != nil {
			t.Errorf("validate idempotency key %q: %v", key, err)
		}
	}
	for _, key := range []string{"", "short", "spaces are bad 123", "line\nbreak", string(bytes.Repeat([]byte{'x'}, 129))} {
		if err := validateIdempotencyKey(key); err == nil {
			t.Errorf("validateIdempotencyKey() accepted %q", key)
		}
	}
}

func TestBuildRequestFingerprintIsStableAndScopeSensitive(t *testing.T) {
	request := BuildRequest{ContentID: "demo:item/potion", Revision: 2, BodyHash: string(bytes.Repeat([]byte{'a'}, 64))}
	first, err := buildRequestFingerprint("demo", request)
	if err != nil {
		t.Fatalf("fingerprint valid request: %v", err)
	}
	second, err := buildRequestFingerprint("demo", request)
	if err != nil {
		t.Fatalf("fingerprint replay: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("identical requests produced different idempotency fingerprints")
	}
	otherGame, err := buildRequestFingerprint("other-game", request)
	if err != nil {
		t.Fatalf("fingerprint other Game request: %v", err)
	}
	if bytes.Equal(first, otherGame) {
		t.Fatal("Game scope was not included in request fingerprint")
	}
	request.BodyHash = string(bytes.Repeat([]byte{'b'}, 64))
	otherPayload, err := buildRequestFingerprint("demo", request)
	if err != nil {
		t.Fatalf("fingerprint different request: %v", err)
	}
	if bytes.Equal(first, otherPayload) {
		t.Fatal("payload change was not included in request fingerprint")
	}
}
