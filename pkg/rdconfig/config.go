// Package rdconfig manages the rd configuration files:
//   - ~/.campfire/rd.json (global Config)
//   - <project>/.ready/config.json (project-local SyncConfig)
package rdconfig

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Config holds rd-specific configuration persisted across sessions.
type Config struct {
	// Org is the organization name used for namespace registration (e.g. "acme").
	// The ready namespace is cf://<org>.ready.
	Org string `json:"org,omitempty"`

	// HomeCampfireID is the hex campfire ID of the operator root / home campfire.
	HomeCampfireID string `json:"home_campfire_id,omitempty"`

	// ReadyCampfireID is the hex campfire ID of the cf://<org>.ready namespace campfire.
	ReadyCampfireID string `json:"ready_campfire_id,omitempty"`

	// RelayEndpoints lists the self-hosted nostr relays (strfry) the portfolio
	// identity reads/writes for the nostr migration. When empty, DefaultRelays()
	// is used. See relay.go and docs/relay-runbook.md. The relays are a cache /
	// always-available copy, NEVER the source of truth.
	RelayEndpoints []RelayEndpoint `json:"relay_endpoints,omitempty"`

	// TrustedPubkeys is the nostr web-of-trust allowlist (ready-d53): the hex
	// secp256k1 pubkeys of ADMITTED portfolio identities (maintainers / other
	// machines) whose signed events are authorized to mutate rd work-item state.
	//
	// This is the trust source for the read-side authorization gate. nostr's
	// Event.Verify() only proves an event is internally consistent (id + schnorr
	// sig) — it proves NOTHING about WHO may write. Any generated key produces
	// events that Verify. So at ingestion (relay reconcile) and projection
	// (replay), rd drops every event whose author pubkey is not in the trust set.
	//
	// The self (portfolio) pubkey is ALWAYS trusted implicitly (see TrustSet); it
	// need not be listed here. Admit another identity by adding its hex pubkey.
	// An empty list means "self only" — a single-machine portfolio trusts just
	// its own key, which is the safe default (a permissive relay cannot inject
	// state authored by a foreign key). Relay-side write-allowlisting (ready-266)
	// is a SEPARATE, defence-in-depth layer; this client-side gate stands alone.
	TrustedPubkeys []string `json:"trusted_pubkeys,omitempty"`
}

// TrustSet returns the read-side authorization allowlist as a set: the self
// (portfolio) pubkey — always trusted — unioned with every admitted pubkey in
// TrustedPubkeys. selfPubkey is the hex pubkey of the loaded portfolio key; pass
// "" only when there is no local identity (the set is then exactly the configured
// admitted pubkeys). Empty/blank entries are ignored.
//
// The returned map is always non-nil, so callers can pass it straight to the
// ingestion / projection trust gate: a non-nil trust set ENFORCES the allowlist
// (events from unlisted authors are dropped), whereas a nil set disables the gate
// (used only by tests / legacy unconfigured paths).
func (c *Config) TrustSet(selfPubkey string) map[string]bool {
	set := make(map[string]bool, len(c.TrustedPubkeys)+1)
	if selfPubkey != "" {
		set[selfPubkey] = true
	}
	for _, pk := range c.TrustedPubkeys {
		if pk != "" {
			set[pk] = true
		}
	}
	return set
}

// Path returns the config file path within the given campfire home directory.
func Path(cfHome string) string {
	return filepath.Join(cfHome, "rd.json")
}

// Load reads the config from disk. Returns a zero Config if the file doesn't exist.
func Load(cfHome string) (*Config, error) {
	data, err := os.ReadFile(Path(cfHome))
	if os.IsNotExist(err) {
		return &Config{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}
	return &c, nil
}

// Save writes the config to disk.
func Save(cfHome string, c *Config) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding config: %w", err)
	}
	return os.WriteFile(Path(cfHome), data, 0600)
}

// DurabilityAssessment holds the result of a durability evaluation stored
// alongside sync config so callers can audit what was assessed at init time.
type DurabilityAssessment struct {
	// MeetsMinimum is true when the campfire meets the minimum durability
	// requirements (max-ttl:0 + lifecycle:persistent + provenance:basic or higher).
	MeetsMinimum bool `json:"meets_minimum"`

	// Weight is the qualitative trust weight: "high", "medium", "low",
	// "minimal", or "unknown".
	Weight string `json:"weight"`

	// MaxTTL is the parsed max-ttl value from the beacon tags ("0", "30d", etc.)
	// Empty if no max-ttl tag was present.
	MaxTTL string `json:"max_ttl,omitempty"`

	// LifecycleType is the parsed lifecycle type ("persistent", "ephemeral",
	// "bounded") or empty if no lifecycle tag was present.
	LifecycleType string `json:"lifecycle_type,omitempty"`

	// Warnings lists advisory messages from parse and trust evaluation.
	Warnings []string `json:"warnings,omitempty"`

	// ProvenanceLevel is the provenance level used for evaluation.
	ProvenanceLevel string `json:"provenance_level,omitempty"`
}

// SyncConfig holds project-local sync configuration stored in
// <project>/.ready/config.json.
type SyncConfig struct {
	// CampfireID is the hex campfire ID this project syncs to.
	CampfireID string `json:"campfire_id,omitempty"`

	// ProjectName is the human-readable name of the project used to resolve the
	// campfire via the naming authority (e.g., "acme.ready" convention).
	ProjectName string `json:"project_name,omitempty"`

	// SummaryCampfireID is the hex campfire ID of the shadow summary campfire.
	// The convention server writes work:item-summary projections here on every
	// consequential operation (create, close, claim). Org observers are admitted
	// to this campfire (not the main campfire) via 'rd admit --role org-observer'.
	// Created by 'rd init' alongside the main campfire.
	SummaryCampfireID string `json:"summary_campfire_id,omitempty"`

	// Encrypted, when true, indicates the main campfire was created with E2E
	// encryption enabled. The campfire SDK's encryption support may be stubbed
	// depending on the SDK version; this field records the intent.
	Encrypted bool `json:"encrypted,omitempty"`

	// InboxCampfireID is the hex campfire ID of the maintainer inbox campfire.
	// When set, the convention server watches this campfire for incoming
	// join-request messages and materializes work:join-request items in the
	// project campfire.
	InboxCampfireID string `json:"inbox_campfire_id,omitempty"`

	// RelayURL is the HTTP relay endpoint used to create or join this project's
	// campfires. Non-empty when the project was initialized with a relay transport.
	// rd sync uses this to reach the campfire when the relay is not discoverable
	// from beacon metadata alone.
	RelayURL string `json:"relay_url,omitempty"`

	// Beacon is the portable beacon string (beacon:BASE64) for the main project
	// campfire. Used by rd init --join on other machines to join the same project.
	Beacon string `json:"beacon,omitempty"`

	// Durability is the durability assessment at configuration time.
	Durability *DurabilityAssessment `json:"durability,omitempty"`

	// Board is the PINNED authoritative nostr board coordinate
	// "30301:<ownerPubkey>:<boardD>" for this project (BP-3, design
	// docs/design/nostr-identity-model.md §4). When set, the nostr projection
	// rejects any card whose "a" coordinate is not this board — closing the
	// parallel-board self-escalation path (any relay-admitted key otherwise forks
	// its own 30301 and self-grants maintainer). Empty = unpinned (no card is
	// rejected on board grounds); existing installs load with an empty Board and are
	// therefore unaffected until the pin is written.
	Board string `json:"board,omitempty"`
}

// SyncConfigPath returns the path to the project-local sync config file.
func SyncConfigPath(projectDir string) string {
	return filepath.Join(projectDir, ".ready", "config.json")
}

// LoadSyncConfig reads the project-local sync config. Returns a zero SyncConfig
// if the file does not exist.
func LoadSyncConfig(projectDir string) (*SyncConfig, error) {
	data, err := os.ReadFile(SyncConfigPath(projectDir))
	if os.IsNotExist(err) {
		return &SyncConfig{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading sync config: %w", err)
	}
	var c SyncConfig
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parsing sync config: %w", err)
	}
	return &c, nil
}

// SaveSyncConfig writes the project-local sync config.
func SaveSyncConfig(projectDir string, c *SyncConfig) error {
	dir := filepath.Join(projectDir, ".ready")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("creating .ready dir: %w", err)
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding sync config: %w", err)
	}
	return os.WriteFile(SyncConfigPath(projectDir), data, 0600)
}
