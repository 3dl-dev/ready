package main

// party_case_test.go — ready-3e1, the PARTY-TOKEN half of the normalization
// sweep: --for / --by / --to.
//
// The pubkey-only entry points (grant/revoke/kill/board share/follow/link/
// --scope/--add-key) are pinned by their own tests next to each site. This file
// covers group B of normalizeHexPubkey's enumeration (cmd/rd/join.go), the last
// hole the veracity audit found, and it is the WORST-SHAPED one:
//
//   - `rd create --for <UPPERCASE>` does not merely mis-answer a local query. It
//     SIGNS the non-canonical pubkey into the kind-30302 card's "for" tag, and
//     that event travels to every reader of the board, permanently. Same for
//     --by / `rd delegate --to` (the card's "p" tag) and `rd engage --for`.
//   - pkg/views matches parties by BYTE-SET MEMBERSHIP (idset[item.For],
//     idset[item.By]), and no identity comparison anywhere is case-insensitive:
//     `grep -rn EqualFold pkg/` is empty, and the three strings.EqualFold uses
//     under cmd/ are a URL scheme check, the parent-id "none" sentinel and an
//     `rd init` prompt answer. So the real holder of that key gets "nothing
//     ready" / "nothing active" on every machine, while rd blames the queue, not
//     the case difference. It cannot be repaired at the read side — the tag is
//     signed.
//
// EVERY test here drives the real cobra command (createCmd/engageCmd/
// delegateCmd/readyCmd/listCmd/workCmd RunE), because the normalization lives in
// the command body. The write-side tests then assert on the tag of the SIGNED
// CARD read back off the authoritative nostr log — the distributed artifact
// itself, not an in-memory struct.
//
// The guard is as load-bearing as the normalization: a party token may equally
// be an email, an agent id, or a cf:// URI, so TestCreateCmd_MixedCaseEmailFor_
// IsNotCorrupted pins that non-pubkey tokens survive byte-identical. Replacing
// normalizePartyToken with an unconditional strings.ToLower turns that test red.

import (
	"strings"
	"testing"

	"github.com/spf13/pflag"

	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/playbook"
	"github.com/3dl-dev/ready/pkg/state"
	rdSync "github.com/3dl-dev/ready/pkg/sync"
)

// setCreateFlags sets createCmd's flags for one RunE call and returns a restore
// func that also clears pflag's Changed bit. Clearing Changed matters: createCmd
// is a package-level singleton and runCreateNostr branches on
// cmd.Flags().Changed("for") — a leaked Changed=true with an empty value makes
// the NEXT test in the package fail with `--for: value cannot be empty`.
func setCreateFlags(t *testing.T, kv map[string]string) func() {
	t.Helper()
	for name, val := range kv {
		if err := createCmd.Flags().Set(name, val); err != nil {
			t.Fatalf("setting --%s=%q: %v", name, val, err)
		}
	}
	return func() {
		for name := range kv {
			if err := createCmd.Flags().Set(name, ""); err != nil {
				t.Fatalf("resetting --%s: %v", name, err)
			}
			createCmd.Flags().Lookup(name).Changed = false
		}
	}
}

// cardTagForItem returns the value of tag `name` on the kind-30302 card whose
// "d" tag is itemID, read out of the project's authoritative nostr log. This is
// the SIGNED wire value — what every other reader of this board will see.
func cardTagForItem(t *testing.T, dir, itemID, name string) string {
	t.Helper()
	events, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
	if err != nil {
		t.Fatalf("ReadAll nostr log: %v", err)
	}
	var found string
	var sawCard bool
	for _, e := range events {
		if e.Kind != rdSync.KindCard {
			continue
		}
		if d, ok := tagVal(e.Tags, "d"); !ok || d != itemID {
			continue
		}
		sawCard = true
		if v, ok := tagVal(e.Tags, name); ok {
			found = v
		}
	}
	if !sawCard {
		t.Fatalf("no kind-%d card with d=%q in the nostr log at %s", rdSync.KindCard, itemID, rdSync.NostrLogPath(dir))
	}
	return found
}

// TestCreateCmd_UppercaseHexFor_SignsCanonicalForTag pins cmd/rd/create.go's
// --for. Reverting `forParty := normalizePartyToken(forPartyRaw)` to the bare
// GetString turns this red: the card's signed "for" tag carries the uppercase
// string, which no reader's byte-set can ever match.
func TestCreateCmd_UppercaseHexFor_SignsCanonicalForTag(t *testing.T) {
	dir, owner := setupNostrNativeProject(t)

	upper := strings.ToUpper(owner)
	if upper == owner {
		t.Fatalf("precondition: owner pubkey %q has no letters to upper-case; regenerate", owner)
	}
	restore := setCreateFlags(t, map[string]string{
		"id": "forcase-001", "type": "task", "priority": "p2", "for": upper,
	})
	defer restore()

	_ = captureStdoutPipe(t, func() {
		if err := createCmd.RunE(createCmd, []string{"Uppercase for-party"}); err != nil {
			t.Fatalf("createCmd.RunE: %v", err)
		}
	})

	if got := cardTagForItem(t, dir, "forcase-001", "for"); got != owner {
		t.Errorf("signed card \"for\" tag = %q; want the canonical lowercase %q.\n"+
			"`rd create --for <UPPERCASE-hex>` signed a non-canonical pubkey into a distributed "+
			"kind-30302 event: every reader byte-matches item.For against lowercase event pubkeys, "+
			"so the real holder of that key is told \"nothing ready\" forever and the tag cannot be "+
			"repaired at the read side.", got, owner)
	}

	// And the projection the caller reads back agrees — the same string the web
	// board and every other machine will index on.
	item, err := nostrResolveItem("forcase-001")
	if err != nil {
		t.Fatalf("nostrResolveItem: %v", err)
	}
	if item.For != owner {
		t.Errorf("projected item.For = %q; want %q", item.For, owner)
	}
	assertNoDotCf(t)
}

// TestCreateCmd_UppercaseHexBy_SignsCanonicalAssigneeTag pins cmd/rd/create.go's
// --by. item.By becomes CardSpec.Assignee, which is emitted as the card's "p"
// tag (pkg/sync/nostrwire.go) — the SAME tag class as the dead-grant defect this
// item was filed for. Reverting `by := normalizePartyToken(byRaw)` turns it red.
func TestCreateCmd_UppercaseHexBy_SignsCanonicalAssigneeTag(t *testing.T) {
	dir, owner := setupNostrNativeProject(t)

	performer := freshKeyHex(t)
	upper := strings.ToUpper(performer)
	restore := setCreateFlags(t, map[string]string{
		"id": "bycase-001", "type": "task", "priority": "p2", "by": upper,
	})
	defer restore()

	_ = captureStdoutPipe(t, func() {
		if err := createCmd.RunE(createCmd, []string{"Uppercase by-party"}); err != nil {
			t.Fatalf("createCmd.RunE: %v", err)
		}
	})

	if got := cardTagForItem(t, dir, "bycase-001", "p"); got != performer {
		t.Errorf("signed card \"p\" (assignee) tag = %q; want the canonical lowercase %q — "+
			"`rd work --for <the performer's real key>` byte-matches idset[item.By] and finds nothing",
			got, performer)
	}
	// --for was NOT passed, so it must still default to the signer (proving the
	// normalization did not disturb the forChanged default path).
	if got := cardTagForItem(t, dir, "bycase-001", "for"); got != owner {
		t.Errorf("card \"for\" tag = %q; want the signer default %q", got, owner)
	}
	assertNoDotCf(t)
}

// TestCreateCmd_MixedCaseEmailFor_IsNotCorrupted is the GUARD half, and it is a
// real-pipeline test, not a unit assertion on the helper: a party token may be an
// email, an agent id, or a cf:// URI, and item.For is stored verbatim for those.
// Replacing normalizePartyToken's `len==64 && isHex` guard with an unconditional
// strings.ToLower turns this red — trading ready-3e1's defect for its mirror
// image, where `rd create --for Baron@3DL.dev` is filed under a party the caller
// never named.
func TestCreateCmd_MixedCaseEmailFor_IsNotCorrupted(t *testing.T) {
	dir, _ := setupNostrNativeProject(t)

	for _, tc := range []struct {
		name, id, token string
	}{
		{"mixed_case_email", "guardcase-001", "Baron@3DL.dev"},
		{"agent_id_with_caps", "guardcase-002", "atlas/Worker-3"},
		{"cf_uri_with_caps", "guardcase-003", "cf://agents/Implementer"},
		// 64 chars but NOT hex — length alone must not trigger the lowercase.
		{"64_char_non_hex", "guardcase-004", strings.Repeat("Z", 64)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			restore := setCreateFlags(t, map[string]string{
				"id": tc.id, "type": "task", "priority": "p2", "for": tc.token,
			})
			defer restore()

			_ = captureStdoutPipe(t, func() {
				if err := createCmd.RunE(createCmd, []string{"Guarded party token"}); err != nil {
					t.Fatalf("createCmd.RunE: %v", err)
				}
			})

			if got := cardTagForItem(t, dir, tc.id, "for"); got != tc.token {
				t.Errorf("signed card \"for\" tag = %q; want the token BYTE-IDENTICAL as typed (%q). "+
					"A non-pubkey party token must never be case-folded — doing so files the item "+
					"under a party the caller never named.", got, tc.token)
			}
		})
	}
}

// TestEngageCmd_UppercaseHexFor_SignsCanonicalForTag pins cmd/rd/engage.go's
// --for. engage is `create` multiplied by the playbook: one non-canonical "for"
// tag per instantiated item, all signed. Reverting engage.go's
// normalizePartyToken call turns this red.
func TestEngageCmd_UppercaseHexFor_SignsCanonicalForTag(t *testing.T) {
	dir, owner := setupNostrNativeProject(t)

	tmpl := &playbook.PlaybookTemplate{
		ID:    "casebook",
		Title: "Case Book",
		Items: []playbook.TemplateItem{
			{Title: "Engaged step one", Type: "task", Priority: "p2"},
			{Title: "Engaged step two", Type: "task", Priority: "p2"},
		},
	}
	if err := playbooksStore(dir).Add(tmpl); err != nil {
		t.Fatalf("Add template: %v", err)
	}

	// --var is a StringArray whose Set() APPENDS; reset via SliceValue.Replace so
	// repeated runs stay deterministic (engageCmd is a package singleton).
	varFlag := engageCmd.Flags().Lookup("var").Value.(pflag.SliceValue)
	if err := varFlag.Replace(nil); err != nil {
		t.Fatalf("reset --var: %v", err)
	}
	t.Cleanup(func() { _ = varFlag.Replace(nil) })

	upper := strings.ToUpper(owner)
	if err := engageCmd.Flags().Set("for", upper); err != nil {
		t.Fatalf("setting engage --for: %v", err)
	}
	t.Cleanup(func() {
		if err := engageCmd.Flags().Set("for", ""); err != nil {
			t.Fatalf("resetting engage --for: %v", err)
		}
		engageCmd.Flags().Lookup("for").Changed = false
	})

	_ = captureStdoutPipe(t, func() {
		if err := engageCmd.RunE(engageCmd, []string{"casebook"}); err != nil {
			t.Fatalf("engageCmd.RunE: %v", err)
		}
	})

	_, byID, err := nostrProjectAllItems()
	if err != nil {
		t.Fatalf("nostrProjectAllItems: %v", err)
	}
	var checked int
	for _, it := range byID {
		if it.Title != "Engaged step one" && it.Title != "Engaged step two" {
			continue
		}
		checked++
		if got := cardTagForItem(t, dir, it.ID, "for"); got != owner {
			t.Errorf("engaged item %s (%q): signed card \"for\" tag = %q; want canonical %q",
				it.ID, it.Title, got, owner)
		}
	}
	if checked != 2 {
		t.Fatalf("found %d of the 2 engaged items in the projection; the fixture did not instantiate", checked)
	}
	assertNoDotCf(t)
}

// TestDelegateCmd_UppercaseHexTo_SignsCanonicalAssigneeTag pins
// cmd/rd/delegate.go's --to. runDelegateNostr assigns item.By = to and
// republishes the card, so an uppercase pubkey is signed into the "p" tag and
// the delegatee's own `rd work` never sees the item delegated to them.
// Reverting delegate.go's normalizePartyToken call turns this red.
func TestDelegateCmd_UppercaseHexTo_SignsCanonicalAssigneeTag(t *testing.T) {
	dir, _ := setupNostrNativeProject(t)

	id, err := runCreateNostr(dir, nostrCreateSpec{
		id: "delegcase-001", title: "To be delegated", itemType: "task", priority: "p2",
	})
	if err != nil {
		t.Fatalf("runCreateNostr: %v", err)
	}

	delegatee := freshKeyHex(t)
	upper := strings.ToUpper(delegatee)
	if err := delegateCmd.Flags().Set("to", upper); err != nil {
		t.Fatalf("setting delegate --to: %v", err)
	}
	t.Cleanup(func() {
		if err := delegateCmd.Flags().Set("to", ""); err != nil {
			t.Fatalf("resetting delegate --to: %v", err)
		}
		delegateCmd.Flags().Lookup("to").Changed = false
	})

	_ = captureStdoutPipe(t, func() {
		if err := delegateCmd.RunE(delegateCmd, []string{id}); err != nil {
			t.Fatalf("delegateCmd.RunE: %v", err)
		}
	})

	if got := cardTagForItem(t, dir, id, "p"); got != delegatee {
		t.Errorf("after `rd delegate --to <UPPERCASE-hex>`, signed card \"p\" tag = %q; want the "+
			"canonical lowercase %q — the delegatee's `rd work --for <their key>` byte-matches "+
			"idset[item.By] and reports \"nothing active\"", got, delegatee)
	}
	item, err := nostrResolveItem(id)
	if err != nil {
		t.Fatalf("nostrResolveItem: %v", err)
	}
	if item.By != delegatee {
		t.Errorf("projected item.By = %q; want %q", item.By, delegatee)
	}
	assertNoDotCf(t)
}

// TestReadyCmd_UppercaseHexFor_FindsTheCallersItem pins cmd/rd/ready.go:63's
// --for (distinct from --scope, already covered by
// TestReadyCmd_RunE_UppercaseScopeKey_IsNotDenied). Reverting
// `forFilter := normalizePartyToken(forFilterRaw)` turns this red: the item
// exists, is ready, and belongs to the caller, and rd prints "nothing ready".
func TestReadyCmd_UppercaseHexFor_FindsTheCallersItem(t *testing.T) {
	origTTY := isTTYStdout
	defer func() { isTTYStdout = origTTY }()
	isTTYStdout = func() bool { return true }
	origJSON := jsonOutput
	defer func() { jsonOutput = origJSON }()
	jsonOutput = false

	dir := setupNostrProjectWithItems(t, "readyforcase", nil)
	k, err := nostrKey()
	if err != nil {
		t.Fatalf("nostrKey: %v", err)
	}
	ownHex := k.PubKeyHex()
	item := &state.Item{ID: "readyforcase-1", Status: "active", Priority: "p2", Title: "Mine", For: ownHex, By: ownHex}
	if err := publishItemFullCreateNostr(dir, ownHex, item); err != nil {
		t.Fatalf("publish own item: %v", err)
	}

	defer resetReadyRunFlags(t)()
	defer setForFilter(t, strings.ToUpper(ownHex))()

	stdout, _ := runReadyCapturingBoth(t)
	if !strings.Contains(stdout, "readyforcase-1") {
		t.Errorf("`rd ready --for <UPPERCASE-hex>` stdout = %q; want the caller's own ready item "+
			"listed. nostrPartyIdentitySet's map lookup and idset[item.For] are byte comparisons "+
			"against lowercase event pubkeys, so an unnormalized --for zeroes the caller's own queue.",
			stdout)
	}
}

// TestListCmd_UppercaseHexForAndBy_FindTheItem pins cmd/rd/list.go:44-45. --for
// goes through nostrPartyIdentitySet + idset[item.For]; --by goes through
// applyListFilters' exact `item.By != byFilter` comparison. Reverting either
// normalizePartyToken call turns its subtest red.
func TestListCmd_UppercaseHexForAndBy_FindTheItem(t *testing.T) {
	origJSON := jsonOutput
	defer func() { jsonOutput = origJSON }()
	jsonOutput = false

	dir := setupNostrProjectWithItems(t, "listcase", nil)
	k, err := nostrKey()
	if err != nil {
		t.Fatalf("nostrKey: %v", err)
	}
	ownHex := k.PubKeyHex()
	item := &state.Item{ID: "listcase-1", Status: "inbox", Priority: "p2", Title: "Mine", For: ownHex, By: ownHex}
	if err := publishItemFullCreateNostr(dir, ownHex, item); err != nil {
		t.Fatalf("publish own item: %v", err)
	}

	for _, flagName := range []string{"for", "by"} {
		t.Run(flagName, func(t *testing.T) {
			if err := listCmd.Flags().Set(flagName, strings.ToUpper(ownHex)); err != nil {
				t.Fatalf("setting list --%s: %v", flagName, err)
			}
			t.Cleanup(func() {
				if err := listCmd.Flags().Set(flagName, ""); err != nil {
					t.Fatalf("resetting list --%s: %v", flagName, err)
				}
			})

			stdout := captureStdoutPipe(t, func() {
				if err := listCmd.RunE(listCmd, nil); err != nil {
					t.Fatalf("listCmd.RunE: %v", err)
				}
			})
			if !strings.Contains(stdout, "listcase-1") {
				t.Errorf("`rd list --%s <UPPERCASE-hex>` stdout = %q; want listcase-1 listed — "+
					"the filter byte-compares against the lowercase pubkey stored on the item",
					flagName, stdout)
			}
		})
	}
}

// TestWorkCmd_UppercaseHexFor_FindsTheActiveItem pins cmd/rd/work.go:17. --for
// reaches views.MyWorkFilter, whose predicate is idset[item.By]. Reverting
// work.go's normalizePartyToken call turns this red and rd prints
// "nothing active" for an identity that is actively working an item.
func TestWorkCmd_UppercaseHexFor_FindsTheActiveItem(t *testing.T) {
	origJSON := jsonOutput
	defer func() { jsonOutput = origJSON }()
	jsonOutput = false

	dir := setupNostrProjectWithItems(t, "workcase", nil)
	k, err := nostrKey()
	if err != nil {
		t.Fatalf("nostrKey: %v", err)
	}
	ownHex := k.PubKeyHex()
	item := &state.Item{ID: "workcase-1", Status: "active", Priority: "p2", Title: "Mine", For: ownHex, By: ownHex}
	if err := publishItemFullCreateNostr(dir, ownHex, item); err != nil {
		t.Fatalf("publish own item: %v", err)
	}

	if err := workCmd.Flags().Set("for", strings.ToUpper(ownHex)); err != nil {
		t.Fatalf("setting work --for: %v", err)
	}
	t.Cleanup(func() {
		if err := workCmd.Flags().Set("for", ""); err != nil {
			t.Fatalf("resetting work --for: %v", err)
		}
	})

	stdout := captureStdoutPipe(t, func() {
		if err := workCmd.RunE(workCmd, nil); err != nil {
			t.Fatalf("workCmd.RunE: %v", err)
		}
	})
	if !strings.Contains(stdout, "workcase-1") {
		t.Errorf("`rd work --for <UPPERCASE-hex>` stdout = %q; want workcase-1 listed — "+
			"views.MyWorkFilter's idset[item.By] is a byte comparison", stdout)
	}
}

// TestCreateUppercaseFor_ThenReadyLowercaseFor_FindsTheItem is the END-TO-END
// proof the item's done clause asks for, and it is the exact reproduction the
// veracity audit ran: WRITE with an uppercase --for, then READ as the real
// holder of that key with the canonical lowercase form, on a DIFFERENT machine
// identity than the writer.
//
// The two sides are deliberately independent: the writer signs with the board
// owner key, the reader is a freshly generated key whose canonical hex is what
// the item was filed under. Before the fix the reader saw "nothing ready" on
// stdout and "0 items ready for <lowercase>… 1 item(s) exist for other parties"
// on stderr — rd blaming the queue for a case difference in a signed tag. This
// test therefore fails if EITHER side is reverted: create.go's --for (the tag is
// signed uppercase, so no lowercase reader can match it) or ready.go's --for
// (superfluous here, since the reader passes lowercase — hence it is pinned
// separately above).
func TestCreateUppercaseFor_ThenReadyLowercaseFor_FindsTheItem(t *testing.T) {
	origTTY := isTTYStdout
	defer func() { isTTYStdout = origTTY }()
	isTTYStdout = func() bool { return true }
	origJSON := jsonOutput
	defer func() { jsonOutput = origJSON }()
	jsonOutput = false

	setupNostrNativeProject(t)

	// The party the item is FOR is a real secp256k1 identity that is not the
	// writer — so "the reader finds it" cannot be satisfied by the signer default.
	recipient, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	canonical := recipient.PubKeyHex()

	restore := setCreateFlags(t, map[string]string{
		"id": "e2ecase-001", "type": "task", "priority": "p2", "for": strings.ToUpper(canonical),
	})
	defer restore()

	_ = captureStdoutPipe(t, func() {
		if err := createCmd.RunE(createCmd, []string{"Filed for the uppercase form"}); err != nil {
			t.Fatalf("createCmd.RunE: %v", err)
		}
	})

	// Now read as the real holder of that key: canonical lowercase --for.
	defer resetReadyRunFlags(t)()
	defer setForFilter(t, canonical)()

	stdout, stderr := runReadyCapturingBoth(t)
	if !strings.Contains(stdout, "e2ecase-001") {
		t.Errorf("`rd create --for <UPPERCASE>` then `rd ready --for <lowercase>`: stdout = %q "+
			"(stderr = %q); want e2ecase-001 listed. The item was filed for this exact key and the "+
			"holder cannot see it — the non-canonical pubkey is signed into the card's \"for\" tag "+
			"and travels to every reader.", stdout, stderr)
	}
	if strings.Contains(stderr, "exist for other parties") {
		t.Errorf("stderr = %q; rd told the rightful owner of the key that the item belongs to "+
			"another party", stderr)
	}
}
