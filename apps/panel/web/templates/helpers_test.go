package templates

import (
	"slices"
	"strings"
	"testing"
)

func TestParseJSONSorted(t *testing.T) {
	data := `{"zebra": 1, "alpha": {"gamma": 2, "beta": 3}, "middle": [1, 2]}`
	items := parseJSON(data)

	var keys []string
	for _, kv := range items {
		keys = append(keys, kv.Key)
	}

	if !slices.IsSorted(keys) {
		t.Fatalf("keys not sorted: %v", keys)
	}

	want := []string{"alpha.beta", "alpha.gamma", "middle", "zebra"}
	if len(keys) != len(want) {
		t.Fatalf("got %d keys %v, want %d: %v", len(keys), keys, len(want), want)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("key %d = %q, want %q", i, keys[i], want[i])
		}
	}
}

func TestParseJSONInvalidEmpty(t *testing.T) {
	for _, data := range []string{"", "{}", "not json"} {
		if items := parseJSON(data); items != nil {
			t.Fatalf("parseJSON(%q) = %v, want nil", data, items)
		}
	}
}

func TestRenderJSONKVMasksSecretsInSortedOrder(t *testing.T) {
	data := `{"token": "supersecret", "zebra": "visible", "alpha": {"api_key": "hush", "beta": 1}}`
	items := parseJSON(data)

	wantKeys := []string{"alpha.api_key", "alpha.beta", "token", "zebra"}
	for i, kv := range items {
		if kv.Key != wantKeys[i] {
			t.Fatalf("key %d = %q, want %q (all keys: %v)", i, kv.Key, wantKeys[i], items)
		}
	}

	for _, kv := range items {
		rendered := renderJSONKV(kv)
		switch kv.Key {
		case "alpha.api_key", "token":
			if strings.Contains(rendered, "supersecret") || strings.Contains(rendered, "hush") {
				t.Fatalf("secret value leaked for key %q: %s", kv.Key, rendered)
			}
			if !strings.Contains(rendered, "••••") {
				t.Fatalf("expected masked value for key %q, got: %s", kv.Key, rendered)
			}
		default:
			if strings.Contains(rendered, "••••") {
				t.Fatalf("unexpected mask for non-secret key %q: %s", kv.Key, rendered)
			}
		}
	}
}
