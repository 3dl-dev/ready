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
	// URL is the websocket endpoint, e.g. "wss://relay.example.com".
	URL string `json:"url"`

	// Read is true when the identity reads events from this relay.
	Read bool `json:"read"`

	// Write is true when the identity publishes events to this relay.
	Write bool `json:"write"`
}

// DefaultRelays returns no relays. The ship default is LOCAL-ONLY: the
// append-only signed-event log (.ready/nostr-log.jsonl) is the source of truth
// and a project works standalone with no reachable relay. Relays are opt-in —
// chosen at 'rd init' (interactively or via --relay/--local) or added later by
// editing rd.json. The binary bakes in no infrastructure of its own.
func DefaultRelays() []RelayEndpoint {
	return nil
}

// Relays returns the configured relay endpoints. When none are set the result
// is empty (local-only). This is the single accessor downstream code should use
// to discover relay endpoints.
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
