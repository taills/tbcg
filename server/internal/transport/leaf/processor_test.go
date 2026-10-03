package leaf

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	path := filepath.Join("../../../../tests/fixtures/protocol", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return data
}

func TestProcessorUnmarshalUsesCatalogIDs(t *testing.T) {
	processor := NewProcessor()
	request, err := processor.Unmarshal(readFixture(t, "hello.request.json"))
	if err != nil {
		t.Fatalf("Unmarshal hello: %v", err)
	}
	got, ok := request.(Request)
	if !ok {
		t.Fatalf("Unmarshal returned %T, want Request", request)
	}
	if got.ID != MessageHello || got.Type != "system.hello" || got.RequestID != "req-hello-1" {
		t.Fatalf("unexpected hello request ID/type/request_id: %d/%q/%q", got.ID, got.Type, got.RequestID)
	}
}

func TestProcessorRejectsMalformedRequests(t *testing.T) {
	processor := NewProcessor()
	tests := []struct {
		name string
		data []byte
	}{
		{name: "invalid utf8", data: []byte{'{', 0xff, '}'}},
		{name: "invalid json", data: []byte(`{"protocol_version":`)},
		{name: "unknown message", data: readFixture(t, "unknown-type.request.json")},
		{name: "bad version", data: []byte(`{"protocol_version":"2","request_id":"r","type":"system.ping","payload":{"nonce":"n"}}`)},
		{name: "unknown envelope field", data: []byte(`{"protocol_version":"1","request_id":"r","type":"system.ping","payload":{"nonce":"n"},"extra":true}`)},
		{name: "missing request id", data: []byte(`{"protocol_version":"1","type":"system.ping","payload":{"nonce":"n"}}`)},
		{name: "missing payload field", data: []byte(`{"protocol_version":"1","request_id":"r","type":"system.ping","payload":{}}`)},
		{name: "payload not object", data: []byte(`{"protocol_version":"1","request_id":"r","type":"system.ping","payload":[]}`)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := processor.Unmarshal(test.data); err == nil {
				t.Fatal("Unmarshal succeeded, want error")
			}
		})
	}
}

func TestProcessorRejectsInvalidPayloadShapes(t *testing.T) {
	processor := NewProcessor()
	tests := []struct {
		name string
		data []byte
	}{
		{name: "ping nonce is array", data: []byte(`{"protocol_version":"1","request_id":"r","type":"system.ping","payload":{"nonce":["n"]}}`)},
		{name: "ping extra field", data: []byte(`{"protocol_version":"1","request_id":"r","type":"system.ping","payload":{"nonce":"n","extra":true}}`)},
		{name: "hello release is array", data: helloWithPayloadField(t, "release_id", []string{"demo-release-1"})},
		{name: "hello capabilities contains non-string", data: helloWithPayloadField(t, "capabilities", []any{"protocol.v1", 1})},
		{name: "hello extra field", data: helloWithPayloadField(t, "extra", "ignored")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := processor.Unmarshal(test.data); err == nil {
				t.Fatal("Unmarshal succeeded, want payload validation error")
			}
		})
	}
}

func helloWithPayloadField(t *testing.T, key string, value any) []byte {
	t.Helper()
	var envelope map[string]any
	if err := json.Unmarshal(readFixture(t, "hello.request.json"), &envelope); err != nil {
		t.Fatalf("decode hello fixture: %v", err)
	}
	payload := envelope["payload"].(map[string]any)
	payload[key] = value
	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("encode hello fixture: %v", err)
	}
	return encoded
}

func TestProcessorUsesSchemaCharacterLimitForRequestID(t *testing.T) {
	request := []byte(`{"protocol_version":"1","request_id":"` + strings.Repeat("界", 128) + `","type":"system.ping","payload":{"nonce":"n"}}`)
	if _, err := NewProcessor().Unmarshal(request); err != nil {
		t.Fatalf("128-character request_id rejected: %v", err)
	}
	request = []byte(`{"protocol_version":"1","request_id":"` + strings.Repeat("界", 129) + `","type":"system.ping","payload":{"nonce":"n"}}`)
	if _, err := NewProcessor().Unmarshal(request); err == nil {
		t.Fatal("129-character request_id accepted")
	}
}

func TestProcessorUsesSchemaCharacterLimitForResponseRequestID(t *testing.T) {
	response := Response{ID: MessagePong, RequestID: strings.Repeat("界", 128), ServerSeq: "1"}
	if _, err := NewProcessor().Marshal(response); err != nil {
		t.Fatalf("128-character request_id rejected: %v", err)
	}
	response.RequestID = strings.Repeat("界", 129)
	if _, err := NewProcessor().Marshal(response); err == nil {
		t.Fatal("129-character request_id accepted")
	}
}

func TestProcessorAllowsObjectResponseError(t *testing.T) {
	frames, err := NewProcessor().Marshal(Response{
		ID: MessagePong, RequestID: "r", ServerSeq: "1", Error: map[string]any{"code": "probe"},
	})
	if err != nil {
		t.Fatalf("Marshal object error: %v", err)
	}
	validateResponseSchema(t, frames[0])
}

func TestProcessorRejectsInvalidResponseObjectTypes(t *testing.T) {
	for _, response := range []Response{
		{ID: MessagePong, RequestID: "r", ServerSeq: "1", Data: "not object"},
		{ID: MessagePong, RequestID: "r", ServerSeq: "1", Meta: []string{"not object"}},
		{ID: MessagePong, RequestID: "r", ServerSeq: "1", Error: "not object or null"},
	} {
		if _, err := NewProcessor().Marshal(response); err == nil {
			t.Errorf("Marshal accepted invalid response object: %#v", response)
		}
	}
}

func TestProcessorRejectsOversizedMessage(t *testing.T) {
	processor := NewProcessor()
	data := make([]byte, MaxMessageBytes+1)
	if _, err := processor.Unmarshal(data); !errors.Is(err, ErrMessageTooLarge) {
		t.Fatalf("Unmarshal error = %v, want ErrMessageTooLarge", err)
	}
}

func TestProcessorRejectsInvalidResponseSequence(t *testing.T) {
	processor := NewProcessor()
	_, err := processor.Marshal(Response{ID: MessagePong, RequestID: "r", ServerSeq: "01"})
	if err == nil {
		t.Fatal("Marshal accepted a non-canonical server_seq")
	}
}

func TestProcessorMarshalsResponse(t *testing.T) {
	processor := NewProcessor()
	response := Response{
		ID:        MessageHelloAck,
		RequestID: "req-hello-1",
		Success:   true,
		ServerSeq: "1",
		Data:      map[string]any{"release_id": "demo-release-1", "capabilities": []string{"protocol.v1"}},
	}
	frames, err := processor.Marshal(response)
	if err != nil {
		t.Fatalf("Marshal response: %v", err)
	}
	if len(frames) != 1 {
		t.Fatalf("Marshal returned %d frames, want 1", len(frames))
	}
	var got map[string]any
	if err := json.Unmarshal(frames[0], &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got["type"] != "system.hello_ack" || got["request_id"] != "req-hello-1" || got["server_seq"] != "1" {
		t.Fatalf("unexpected response: %s", frames[0])
	}
	if got["state_version"] != nil || got["error"] != nil {
		t.Fatalf("missing nullable response fields: %s", frames[0])
	}
	if _, ok := got["meta"].(map[string]any); !ok {
		t.Fatalf("meta must be an object to satisfy response schema: %s", frames[0])
	}
	validateResponseSchema(t, frames[0])
}

func TestProcessorDefaultsNilResponseObjects(t *testing.T) {
	processor := NewProcessor()
	var nilData map[string]any
	var nilMeta map[string]any
	frames, err := processor.Marshal(Response{
		ID: MessagePong, RequestID: "r", Success: true, ServerSeq: "0",
		Data: nilData, Meta: nilMeta,
	})
	if err != nil {
		t.Fatalf("Marshal empty response: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(frames[0], &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	for _, field := range []string{"data", "meta"} {
		if _, ok := got[field].(map[string]any); !ok {
			t.Errorf("%s must default to an object, got %T", field, got[field])
		}
	}
	validateResponseSchema(t, frames[0])
}

func validateResponseSchema(t *testing.T, response []byte) {
	t.Helper()
	schemaData, err := os.ReadFile(filepath.Join("../../../../contracts/protocol", "response.schema.json"))
	if err != nil {
		t.Fatalf("read response schema: %v", err)
	}
	schemaDocument, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaData))
	if err != nil {
		t.Fatalf("decode response schema: %v", err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	if err := compiler.AddResource("urn:tbcg:protocol:response:v1", schemaDocument); err != nil {
		t.Fatalf("register response schema: %v", err)
	}
	compiled, err := compiler.Compile("urn:tbcg:protocol:response:v1")
	if err != nil {
		t.Fatalf("compile response schema: %v", err)
	}
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(response))
	if err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if err := compiled.Validate(instance); err != nil {
		t.Fatalf("response does not satisfy schema: %v", err)
	}
}
