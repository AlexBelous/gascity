package procobserver

import "testing"

func TestDecodeStrictSkipsIgnoredInternalFieldButRejectsWireKey(t *testing.T) {
	type envelope struct {
		Schema   string `json:"schema"`
		Internal string `json:"-"`
	}
	var got envelope
	if err := DecodeStrict([]byte(`{"schema":"v1"}`), &got); err != nil || got.Schema != "v1" {
		t.Fatalf("decode typed envelope with ignored field: %v, %#v", err, got)
	}
	if err := DecodeStrict([]byte(`{"schema":"v1","Internal":"injected"}`), &got); err == nil {
		t.Fatal("ignored internal field accepted from wire")
	}
}
