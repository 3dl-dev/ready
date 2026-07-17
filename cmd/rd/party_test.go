package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/3dl-dev/ready/pkg/identity"
	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/state"
	rdSync "github.com/3dl-dev/ready/pkg/sync"
)

// resetReadyForFlag restores the shared readyCmd --for flag to its UNSET default
// state (value "" AND Changed=false) so runReady takes the default-to-self branch.
// Cobra flags are package globals shared across tests; a prior test's Set() leaves
// Changed=true, which would otherwise route to the show-all path.
func resetReadyForFlag(t *testing.T) {
	t.Helper()
	f := readyCmd.Flags().Lookup("for")
	if f == nil {
		t.Fatalf("readyCmd has no --for flag")
	}
	if err := f.Value.Set(""); err != nil {
		t.Fatalf("reset --for value: %v", err)
	}
	f.Changed = false
	t.Cleanup(func() {
		_ = f.Value.Set("")
		f.Changed = false
	})
}

// runReadyJSONIDs runs readyCmd with --json and returns the set of item IDs it
// emitted. jsonOutput is toggled on for the call and restored after.
func runReadyJSONIDs(t *testing.T) map[string]bool {
	t.Helper()
	origJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = origJSON })

	out := captureStdoutPipe(t, func() {
		if err := readyCmd.RunE(readyCmd, []string{}); err != nil {
			t.Fatalf("readyCmd.RunE: %v", err)
		}
	})
	var items []map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &items); err != nil {
		t.Fatalf("ready --json not valid JSON: %v; output:\n%s", err, out)
	}
	ids := map[string]bool{}
	for _, it := range items {
		if id, _ := it["id"].(string); id != "" {
			ids[id] = true
		}
	}
	return ids
}

// appendSelfSignedAlias signs a person-alias with the local machine key (the sole
// trust root) declaring that memberHex and the given emails belong to ONE party,
// and appends it to the project's authoritative nostr log. This is the on-disk
// equivalent of `rd identify --add-key memberHex --add-email <handle>`.
func appendSelfSignedAlias(t *testing.T, dir string, memberHex string, emails []string) {
	t.Helper()
	k, err := nostrKey()
	if err != nil {
		t.Fatalf("nostrKey: %v", err)
	}
	ev, err := identity.BuildAliasEvent(k, identity.AliasSpec{
		Handle:  emails[0],
		Pubkeys: []string{memberHex},
		Emails:  emails,
	}, time.Now().Unix())
	if err != nil {
		t.Fatalf("BuildAliasEvent: %v", err)
	}
	if err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).Append(ev); err != nil {
		t.Fatalf("append alias: %v", err)
	}
}

// TestReady_PartyAlias_MatchesForByPartyKeyAndEmail is the ready-99d done
// condition (edge #6): on a follower box whose key is aliased to baron@3dl.dev,
// bare `rd ready` (no --for) returns items carrying for=<party-member-hex> AND
// for=baron@3dl.dev, even though neither equals the local machine pubkey. An item
// for a stranger (outside the party) is excluded.
func TestReady_PartyAlias_MatchesForByPartyKeyAndEmail(t *testing.T) {
	// A second party-member key (a9f766ae… in the spec) and an unrelated stranger.
	member, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey member: %v", err)
	}
	memberHex := member.PubKeyHex()
	stranger, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey stranger: %v", err)
	}
	strangerHex := stranger.PubKeyHex()

	items := []*state.Item{
		{ID: "ready-party1", Title: "For party member hex", Type: "task", For: memberHex, Priority: "p2", Status: state.StatusInbox},
		{ID: "ready-party2", Title: "For party email", Type: "task", For: "baron@3dl.dev", Priority: "p2", Status: state.StatusInbox},
		{ID: "ready-party3", Title: "For a stranger", Type: "task", For: strangerHex, Priority: "p2", Status: state.StatusInbox},
	}
	dir := setupNostrProjectWithItems(t, "partyproj", items)

	// Alias the local machine key + memberHex into the baron@3dl.dev party.
	appendSelfSignedAlias(t, dir, memberHex, []string{"baron@3dl.dev"})

	resetReadyForFlag(t)
	ids := runReadyJSONIDs(t)

	if !ids["ready-party1"] {
		t.Errorf("bare `rd ready` missing for=<party-member-hex> item ready-party1; got %v", ids)
	}
	if !ids["ready-party2"] {
		t.Errorf("bare `rd ready` missing for=baron@3dl.dev item ready-party2; got %v", ids)
	}
	if ids["ready-party3"] {
		t.Errorf("bare `rd ready` returned stranger item ready-party3 (not in party); got %v", ids)
	}
}

// TestList_PartyAlias_ExplicitForExpandsParty covers the list.go half of
// ready-99d: an explicit `rd list --for baron@3dl.dev` matches items whose For is
// ANY pubkey or email in that party (here a party-member hex), not just the
// verbatim email. A stranger's item stays excluded.
func TestList_PartyAlias_ExplicitForExpandsParty(t *testing.T) {
	member, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey member: %v", err)
	}
	memberHex := member.PubKeyHex()
	stranger, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey stranger: %v", err)
	}
	strangerHex := stranger.PubKeyHex()

	items := []*state.Item{
		{ID: "list-party1", Title: "For party member hex", Type: "task", For: memberHex, Priority: "p2", Status: state.StatusInbox},
		{ID: "list-party2", Title: "For party email", Type: "task", For: "baron@3dl.dev", Priority: "p2", Status: state.StatusInbox},
		{ID: "list-party3", Title: "For a stranger", Type: "task", For: strangerHex, Priority: "p2", Status: state.StatusInbox},
	}
	dir := setupNostrProjectWithItems(t, "listpartyproj", items)
	appendSelfSignedAlias(t, dir, memberHex, []string{"baron@3dl.dev"})

	f := listCmd.Flags().Lookup("for")
	if err := f.Value.Set("baron@3dl.dev"); err != nil {
		t.Fatalf("set --for: %v", err)
	}
	f.Changed = true
	t.Cleanup(func() { _ = f.Value.Set(""); f.Changed = false })

	origJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = origJSON })

	out := captureStdoutPipe(t, func() {
		if err := listCmd.RunE(listCmd, []string{}); err != nil {
			t.Fatalf("listCmd.RunE: %v", err)
		}
	})
	var listed []map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &listed); err != nil {
		t.Fatalf("list --json not valid JSON: %v; output:\n%s", err, out)
	}
	ids := map[string]bool{}
	for _, it := range listed {
		if id, _ := it["id"].(string); id != "" {
			ids[id] = true
		}
	}

	if !ids["list-party1"] {
		t.Errorf("`rd list --for baron@3dl.dev` missing for=<party-member-hex> item list-party1; got %v", ids)
	}
	if !ids["list-party2"] {
		t.Errorf("`rd list --for baron@3dl.dev` missing for=baron@3dl.dev item list-party2; got %v", ids)
	}
	if ids["list-party3"] {
		t.Errorf("`rd list --for baron@3dl.dev` returned stranger item list-party3; got %v", ids)
	}
}

// setReadyView sets the readyCmd --view flag for the duration of the test and
// restores it to the default ("ready") afterward. Cobra flags are package
// globals shared across tests.
func setReadyView(t *testing.T, viewName string) {
	t.Helper()
	f := readyCmd.Flags().Lookup("view")
	if f == nil {
		t.Fatalf("readyCmd has no --view flag")
	}
	if err := f.Value.Set(viewName); err != nil {
		t.Fatalf("set --view %q: %v", viewName, err)
	}
	t.Cleanup(func() { _ = f.Value.Set("ready") })
}

// TestReady_PartyAlias_MyWorkView is ready-f0b's my-work half: on a follower box
// aliased into a party, `rd ready --view my-work` matches items whose By is ANY
// pubkey or email in the party — not just the literal local machine pubkey.
func TestReady_PartyAlias_MyWorkView(t *testing.T) {
	member, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey member: %v", err)
	}
	memberHex := member.PubKeyHex()
	stranger, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey stranger: %v", err)
	}
	strangerHex := stranger.PubKeyHex()

	items := []*state.Item{
		{ID: "mw-party1", Title: "By party member hex", Type: "task", For: "someone", By: memberHex, Priority: "p2", Status: state.StatusActive},
		{ID: "mw-party2", Title: "By party email", Type: "task", For: "someone", By: "baron@3dl.dev", Priority: "p2", Status: state.StatusWaiting},
		{ID: "mw-stranger", Title: "By a stranger", Type: "task", For: "someone", By: strangerHex, Priority: "p2", Status: state.StatusActive},
	}
	dir := setupNostrProjectWithItems(t, "mypartyproj", items)
	appendSelfSignedAlias(t, dir, memberHex, []string{"baron@3dl.dev"})

	resetReadyForFlag(t)
	setReadyView(t, "my-work")
	ids := runReadyJSONIDs(t)

	if !ids["mw-party1"] {
		t.Errorf("`rd ready --view my-work` missing by=<party-member-hex> item mw-party1; got %v", ids)
	}
	if !ids["mw-party2"] {
		t.Errorf("`rd ready --view my-work` missing by=baron@3dl.dev item mw-party2; got %v", ids)
	}
	if ids["mw-stranger"] {
		t.Errorf("`rd ready --view my-work` returned stranger item mw-stranger (not in party); got %v", ids)
	}
}

// TestReady_PartyAlias_DelegatedView is ready-f0b's delegated half: on a follower
// box aliased into a party, `rd ready --view delegated` matches items whose For
// is ANY pubkey or email in the party AND whose By is OUTSIDE the party (a real
// delegation to someone else) — an item By'd to ANOTHER member of the SAME party
// is self-work under a different key, not a delegation, and stays excluded.
func TestReady_PartyAlias_DelegatedView(t *testing.T) {
	member, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey member: %v", err)
	}
	memberHex := member.PubKeyHex()
	stranger, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey stranger: %v", err)
	}
	strangerHex := stranger.PubKeyHex()

	items := []*state.Item{
		{ID: "dg-party-hex", Title: "For party member hex, by stranger", Type: "task", For: memberHex, By: strangerHex, Priority: "p2", Status: state.StatusActive},
		{ID: "dg-party-email", Title: "For party email, by stranger", Type: "task", For: "baron@3dl.dev", By: strangerHex, Priority: "p2", Status: state.StatusActive},
		{ID: "dg-within-party", Title: "For party member, by same party (not a real delegation)", Type: "task", For: "baron@3dl.dev", By: memberHex, Priority: "p2", Status: state.StatusActive},
		{ID: "dg-stranger-for", Title: "For a stranger", Type: "task", For: strangerHex, By: memberHex, Priority: "p2", Status: state.StatusActive},
	}
	dir := setupNostrProjectWithItems(t, "delegatedpartyproj", items)
	appendSelfSignedAlias(t, dir, memberHex, []string{"baron@3dl.dev"})

	resetReadyForFlag(t)
	setReadyView(t, "delegated")
	ids := runReadyJSONIDs(t)

	if !ids["dg-party-hex"] {
		t.Errorf("`rd ready --view delegated` missing for=<party-member-hex> item dg-party-hex; got %v", ids)
	}
	if !ids["dg-party-email"] {
		t.Errorf("`rd ready --view delegated` missing for=baron@3dl.dev item dg-party-email; got %v", ids)
	}
	if ids["dg-within-party"] {
		t.Errorf("`rd ready --view delegated` returned dg-within-party (by is within the SAME party — not a delegation); got %v", ids)
	}
	if ids["dg-stranger-for"] {
		t.Errorf("`rd ready --view delegated` returned stranger-for item dg-stranger-for (for is not in party); got %v", ids)
	}
}

// TestReady_NoAlias_MyWorkAndDelegatedUnchanged is the ready-f0b control: on a box
// with NO person-alias, `--view my-work`/`--view delegated` scoping is unchanged
// — the party set collapses to {token}, matching only the raw local machine
// pubkey exactly as before ready-f0b.
func TestReady_NoAlias_MyWorkAndDelegatedUnchanged(t *testing.T) {
	other, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey other: %v", err)
	}
	otherHex := other.PubKeyHex()

	items := []*state.Item{
		{ID: "noalias-mw-self", Title: "By self", Type: "task", For: "someone", By: "SELF", Priority: "p2", Status: state.StatusActive},
		{ID: "noalias-mw-other", Title: "By someone else", Type: "task", For: "someone", By: otherHex, Priority: "p2", Status: state.StatusActive},
		{ID: "noalias-dg-self", Title: "For self, by other", Type: "task", For: "SELF", By: otherHex, Priority: "p2", Status: state.StatusActive},
	}
	dir := setupNostrProjectWithItems(t, "noaliaspartyproj", nil)
	k, err := nostrKey()
	if err != nil {
		t.Fatalf("nostrKey: %v", err)
	}
	self := k.PubKeyHex()
	for _, it := range items {
		if it.By == "SELF" {
			it.By = self
		}
		if it.For == "SELF" {
			it.For = self
		}
		if err := publishItemFullCreateNostr(dir, self, it); err != nil {
			t.Fatalf("publish %s: %v", it.ID, err)
		}
	}

	resetReadyForFlag(t)
	setReadyView(t, "my-work")
	mwIDs := runReadyJSONIDs(t)
	if !mwIDs["noalias-mw-self"] {
		t.Errorf("`rd ready --view my-work` (no alias) missing by=self item; got %v", mwIDs)
	}
	if mwIDs["noalias-mw-other"] {
		t.Errorf("`rd ready --view my-work` (no alias) returned by=other item; got %v", mwIDs)
	}

	resetReadyForFlag(t)
	setReadyView(t, "delegated")
	dgIDs := runReadyJSONIDs(t)
	if !dgIDs["noalias-dg-self"] {
		t.Errorf("`rd ready --view delegated` (no alias) missing for=self,by=other item; got %v", dgIDs)
	}
}

// TestReady_NoAlias_MatchesSelfPubkeyOnly is the ready-99d control: on a box with
// NO person-alias, bare `rd ready` scoping is unchanged — it matches only items
// whose For/By equals the raw local machine pubkey. A party-shaped hex that was
// never aliased must NOT match.
func TestReady_NoAlias_MatchesSelfPubkeyOnly(t *testing.T) {
	other, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey other: %v", err)
	}
	otherHex := other.PubKeyHex()

	// Placeholder For; the self-owned item's For is patched to the real self key
	// once the project (and its machine key) exists.
	items := []*state.Item{
		{ID: "ready-solo1", Title: "For self", Type: "task", For: "SELF", Priority: "p2", Status: state.StatusInbox},
		{ID: "ready-solo2", Title: "For someone else", Type: "task", For: otherHex, Priority: "p2", Status: state.StatusInbox},
	}
	// Resolve the self key first so ready-solo1 can carry it as For.
	dir := setupNostrProjectWithItems(t, "soloproj", nil)
	k, err := nostrKey()
	if err != nil {
		t.Fatalf("nostrKey: %v", err)
	}
	self := k.PubKeyHex()
	items[0].For = self
	for _, it := range items {
		if err := publishItemFullCreateNostr(dir, self, it); err != nil {
			t.Fatalf("publish %s: %v", it.ID, err)
		}
	}

	resetReadyForFlag(t)
	ids := runReadyJSONIDs(t)

	if !ids["ready-solo1"] {
		t.Errorf("bare `rd ready` missing for=<self> item ready-solo1; got %v", ids)
	}
	if ids["ready-solo2"] {
		t.Errorf("bare `rd ready` returned non-self item ready-solo2 with no alias present; got %v", ids)
	}
}
