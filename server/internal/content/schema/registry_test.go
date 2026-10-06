package schema

import (
	"os"
	"testing"
)

func TestRegistryCompilesAllContentTypes(t *testing.T) {
	registry, err := NewRegistry(os.DirFS("../../../../contracts"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	for _, contentType := range ContentTypes {
		if _, err := registry.For(contentType); err != nil {
			t.Fatalf("For(%s): %v", contentType, err)
		}
	}
	if len(registry.Fingerprint()) != 64 {
		t.Fatalf("fingerprint = %q", registry.Fingerprint())
	}
}

func TestContentTypeOf(t *testing.T) {
	cases := map[string]string{"demo:item/healing-potion": "item", "demo:map/training-ground": "map", "demo:npc/guide": "npc"}
	for id, want := range cases {
		got, err := ContentTypeOf(id)
		if err != nil || got != want {
			t.Fatalf("ContentTypeOf(%q) = %q, %v", id, got, err)
		}
	}
	for _, id := range []string{"demo", "demo:unknown/x", "demo:map/", ":map/x"} {
		if _, err := ContentTypeOf(id); err == nil {
			t.Fatalf("ContentTypeOf(%q) succeeded", id)
		}
	}
}
