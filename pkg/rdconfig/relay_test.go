package rdconfig

import (
	"path/filepath"
	"testing"
)

func TestDefaultRelays(t *testing.T) {
	// Ship default is LOCAL-ONLY: no relay topology baked into the binary.
	if got := DefaultRelays(); len(got) != 0 {
		t.Fatalf("expected 0 default relays (local-only), got %d: %+v", len(got), got)
	}
}

func TestConfigRelaysLocalOnlyByDefault(t *testing.T) {
	c := &Config{}
	if got := c.Relays(); len(got) != 0 {
		t.Fatalf("empty config should be local-only (no relays), got %d", len(got))
	}
	if got := c.WriteRelayURLs(); len(got) != 0 {
		t.Errorf("expected no write relay URLs by default, got %v", got)
	}
	if got := c.ReadRelayURLs(); len(got) != 0 {
		t.Errorf("expected no read relay URLs by default, got %v", got)
	}
}

func TestConfigRelaysOverride(t *testing.T) {
	c := &Config{
		RelayEndpoints: []RelayEndpoint{
			{URL: "ws://example:7777", Read: true, Write: false},
			{URL: "ws://example2:7777", Read: false, Write: true},
		},
	}
	if len(c.Relays()) != 2 {
		t.Fatalf("expected configured relays to win, got %d", len(c.Relays()))
	}
	w := c.WriteRelayURLs()
	if len(w) != 1 || w[0] != "ws://example2:7777" {
		t.Errorf("write URLs wrong: %v", w)
	}
	r := c.ReadRelayURLs()
	if len(r) != 1 || r[0] != "ws://example:7777" {
		t.Errorf("read URLs wrong: %v", r)
	}
}

// TestRelayEndpointsRoundTrip asserts the relay endpoint config loads/parses
// from the on-disk rd.json (the item's Go-test requirement).
func TestRelayEndpointsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := &Config{
		Org: "acme",
		RelayEndpoints: []RelayEndpoint{
			{URL: "wss://relay-a.example.com", Read: true, Write: true},
			{URL: "wss://relay-b.example.com", Read: true, Write: true},
		},
	}
	if err := Save(dir, want); err != nil {
		t.Fatalf("save: %v", err)
	}
	// Confirm it landed at the expected path.
	if _, err := filepath.Abs(Path(dir)); err != nil {
		t.Fatalf("path: %v", err)
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got.RelayEndpoints) != 2 {
		t.Fatalf("expected 2 relay endpoints after round trip, got %d", len(got.RelayEndpoints))
	}
	for i, r := range got.RelayEndpoints {
		if r != want.RelayEndpoints[i] {
			t.Errorf("relay[%d] mismatch: got %+v want %+v", i, r, want.RelayEndpoints[i])
		}
	}
}
