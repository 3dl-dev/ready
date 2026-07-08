package rdconfig

// Nostr relay endpoint configuration (ready-efe).
//
// The relays are a self-hosted strfry topology (see docs/relay-runbook.md) that
// acts as a CACHE / always-available copy for the rd nostr migration — NEVER the
// source of truth. This file only surfaces WHERE to read/write; it does not wire
// rd's event mapping (that is downstream item ready-a13).

// RelayEndpoint describes a single nostr relay the portfolio identity may read
// from and/or write to. This mirrors the NIP-65 (kind:10002) outbox model: a
// relay can be write-only, read-only, or both.
type RelayEndpoint struct {
	// URL is the websocket endpoint, e.g. "ws://192.168.2.40:7777".
	URL string `json:"url"`

	// Read is true when the identity reads events from this relay.
	Read bool `json:"read"`

	// Write is true when the identity publishes events to this relay.
	Write bool `json:"write"`
}

// DefaultRelays returns the self-hosted strfry relay topology stood up for
// ready-efe (relay-a and relay-b on the mainframe LAN). Both are read+write so
// that either relay going offline still serves the full set (proven by
// scripts/relay-demo.sh). Callers may override via Config.Relays.
func DefaultRelays() []RelayEndpoint {
	return []RelayEndpoint{
		{URL: "ws://192.168.2.40:7777", Read: true, Write: true},
		{URL: "ws://192.168.2.41:7777", Read: true, Write: true},
	}
}

// Relays returns the configured relay endpoints, falling back to DefaultRelays
// when none are set in the config. This is the single accessor downstream code
// (e.g. ready-a13 rd event mapping) should use to discover relay endpoints.
func (c *Config) Relays() []RelayEndpoint {
	if len(c.RelayEndpoints) > 0 {
		return c.RelayEndpoints
	}
	return DefaultRelays()
}

// WriteRelayURLs returns the URLs of all relays flagged for writing.
func (c *Config) WriteRelayURLs() []string {
	var out []string
	for _, r := range c.Relays() {
		if r.Write {
			out = append(out, r.URL)
		}
	}
	return out
}

// ReadRelayURLs returns the URLs of all relays flagged for reading.
func (c *Config) ReadRelayURLs() []string {
	var out []string
	for _, r := range c.Relays() {
		if r.Read {
			out = append(out, r.URL)
		}
	}
	return out
}
