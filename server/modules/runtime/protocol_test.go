package runtime

import (
	"strings"
	"testing"
	"time"
)

func loadTestCatalog(t *testing.T) *Catalog {
	t.Helper()
	catalog, err := LoadCatalog("../../../contracts/protocol/message_catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func TestCatalogDecodeEnforcesEnvelopeAndWhitelist(t *testing.T) {
	catalog := loadTestCatalog(t)
	valid := `{"protocol_version":"1","request_id":"r1","type":"world.move","payload":{"path":[{"x":1,"y":0}],"state_version":"2"}}`
	request, err := catalog.Decode([]byte(valid))
	if err != nil || request.Type != "world.move" {
		t.Fatalf("valid request rejected: %v", err)
	}
	if catalog.ResponseType("world.move") != "world.move_result" || !catalog.IsServerType("battle.round") {
		t.Fatal("catalog response/push mapping missing")
	}
	cases := map[string]string{
		"unknown field":   `{"protocol_version":"1","request_id":"r1","type":"world.move","payload":{"path":[],"state_version":"1","speed":9}}`,
		"missing field":   `{"protocol_version":"1","request_id":"r1","type":"world.move","payload":{"path":[]}}`,
		"wrong version":   `{"protocol_version":"2","request_id":"r1","type":"world.move","payload":{"path":[],"state_version":"1"}}`,
		"unknown type":    `{"protocol_version":"1","request_id":"r1","type":"admin.grant","payload":{}}`,
		"server type":     `{"protocol_version":"1","request_id":"r1","type":"battle.round","payload":{}}`,
		"empty id":        `{"protocol_version":"1","request_id":"","type":"character.list","payload":{}}`,
		"payload array":   `{"protocol_version":"1","request_id":"r1","type":"character.list","payload":[]}`,
		"trailing json":   `{"protocol_version":"1","request_id":"r1","type":"character.list","payload":{}} {}`,
		"envelope extra":  `{"protocol_version":"1","request_id":"r1","type":"character.list","payload":{},"admin":true}`,
		"oversized frame": `{"protocol_version":"1","request_id":"` + strings.Repeat("a", MaxMessageBytes) + `","type":"character.list","payload":{}}`,
	}
	for name, raw := range cases {
		if _, err := catalog.Decode([]byte(raw)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestStrictPayloadDecodingAndResponses(t *testing.T) {
	var target struct {
		Name string `json:"name"`
	}
	if DecodeStrict([]byte(`{"name":"a","extra":1}`), &target) == nil {
		t.Fatal("unknown payload field accepted")
	}
	encoded, err := encodeResponse("1", "push-1", "system.notice", 1, 3, nil, nil)
	if err != nil || !strings.Contains(string(encoded), `"state_version":"3"`) || !strings.Contains(string(encoded), `"data":{}`) {
		t.Fatalf("response encoding %s %v", encoded, err)
	}
	if !buildAtLeast("0.2.0", "0.1.9") || buildAtLeast("0.1.0", "0.1.1") || buildAtLeast("", "0.1.0") {
		t.Fatal("client build comparison wrong")
	}
	if validKey("short") || !validKey("valid-key-123") || validKey("bad key with spaces") {
		t.Fatal("idempotency key validation wrong")
	}
}

func TestSessionRateLimit(t *testing.T) {
	session := &Session{}
	now := time.Now()
	for i := 0; i < maxRequestsPerSecond; i++ {
		if !session.allowRequest(now) {
			t.Fatal("request within budget rejected")
		}
	}
	if session.allowRequest(now) {
		t.Fatal("request over budget accepted")
	}
	if !session.allowRequest(now.Add(time.Second)) {
		t.Fatal("new window must reset the budget")
	}
}
