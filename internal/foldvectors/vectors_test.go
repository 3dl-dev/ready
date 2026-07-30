package foldvectors_test

// The conformance suite (ready-a13a). It reads the COMMITTED
// testdata/fold.vectors.json — it never regenerates it — and replays every
// vector through rd's live fold (pkg/sync.ProjectItems + pkg/views). Because the
// expectations are committed DATA authored from docs/design/board-fold-spec.md,
// any change to the fold's behaviour turns vectors red; the file cannot silently
// track the implementation.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/3dl-dev/ready/internal/foldvectors"
	"github.com/3dl-dev/ready/pkg/state"
	rdsync "github.com/3dl-dev/ready/pkg/sync"
)

const (
	vectorPath = "../../testdata/fold.vectors.json"
	specPath   = "../../docs/design/board-fold-spec.md"
)

func load(t *testing.T) *foldvectors.File {
	t.Helper()
	f, err := foldvectors.Load(vectorPath)
	if err != nil {
		t.Fatalf("load vectors: %v", err)
	}
	if len(f.Vectors) == 0 {
		t.Fatal("vector file contains no vectors")
	}
	return f
}

// TestFoldConformanceVectors is done-condition 1: every vector runs through the
// live fold and must reproduce its expected items and view sets exactly.
func TestFoldConformanceVectors(t *testing.T) {
	f := load(t)
	// The count is reported here (visible under `go test -v ./internal/foldvectors/`)
	// and floored, so a truncated or half-regenerated vector file fails loudly
	// instead of passing with two vectors.
	t.Logf("fold conformance: %d vectors from %s (spec %s)", len(f.Vectors), vectorPath, f.Spec)
	const minVectors = 30
	if len(f.Vectors) < minVectors {
		t.Fatalf("only %d vectors present, expected at least %d — the vector file looks truncated", len(f.Vectors), minVectors)
	}

	seen := map[string]bool{}
	for _, v := range f.Vectors {
		if seen[v.Name] {
			t.Fatalf("duplicate vector name %q", v.Name)
		}
		seen[v.Name] = true

		t.Run(v.Name, func(t *testing.T) {
			gotItems, gotViews, gotLabels, err := foldvectors.Run(v)
			if err != nil {
				t.Fatalf("run: %v", err)
			}

			if len(gotItems) != len(v.Expect.Items) {
				t.Fatalf("item count: want %d, got %d (%v)", len(v.Expect.Items), len(gotItems), itemIDs(gotItems))
			}
			for i := range v.Expect.Items {
				want := normalize(t, v.Expect.Items[i])
				got := normalize(t, gotItems[i])
				if !reflect.DeepEqual(want, got) {
					t.Errorf("item %d mismatch\n  want: %s\n  got:  %s", i, v.Expect.Items[i], gotItems[i])
				}
			}

			// View membership is compared as a SET: rd's rendered order is not a
			// total order (spec §15.7), so an ordered assertion here would be a
			// flake, not a check.
			for name, want := range v.Expect.Views {
				if !sameSet(want, gotViews[name]) {
					t.Errorf("view %q: want %v, got %v", name, sorted(want), sorted(gotViews[name]))
				}
			}
			for name, got := range gotViews {
				if _, ok := v.Expect.Views[name]; !ok {
					t.Errorf("view %q is not asserted by this vector (fold produced %v)", name, sorted(got))
				}
			}
			for atom, want := range v.Expect.LabelViews {
				if !sameSet(want, gotLabels[atom]) {
					t.Errorf("label view %q: want %v, got %v", atom, sorted(want), sorted(gotLabels[atom]))
				}
			}

			// The derived-keyring expectation (spec §11.10-§11.14). Checked in
			// BOTH directions: an asserted keyring must match the derivation, and
			// a vector that derives one must assert it — otherwise a keyring
			// vector could be silently downgraded to "runs the derivation and
			// looks at none of it".
			gotKeyring, kErr := foldvectors.KeyringFactsFor(v)
			if kErr != nil {
				t.Fatalf("derive keyring: %v", kErr)
			}
			switch {
			case v.Expect.Keyring == nil && gotKeyring != nil:
				t.Errorf("vector uses options.keyring but asserts no expect.keyring (derived %+v)", *gotKeyring)
			case v.Expect.Keyring != nil && gotKeyring == nil:
				t.Error("vector asserts expect.keyring without options.keyring")
			case v.Expect.Keyring != nil && *v.Expect.Keyring != *gotKeyring:
				t.Errorf("keyring: want %+v, got %+v", *v.Expect.Keyring, *gotKeyring)
			}
		})
	}
}

// TestEveryVectorCitesALiveSpecClause is done-condition 2: a vector must name at
// least one spec clause, and every clause it names must actually exist in
// docs/design/board-fold-spec.md. A citation that has rotted is a bug in one of
// the two artifacts, not something to silently ignore.
func TestEveryVectorCitesALiveSpecClause(t *testing.T) {
	raw, err := os.ReadFile(filepath.Clean(specPath))
	if err != nil {
		t.Fatalf("read spec: %v", err)
	}
	clausePattern := regexp.MustCompile(`\*\*§([0-9]+\.[0-9]+)`)
	known := map[string]bool{}
	for _, m := range clausePattern.FindAllStringSubmatch(string(raw), -1) {
		known[m[1]] = true
	}
	if len(known) < 100 {
		t.Fatalf("only %d clauses parsed from %s — the spec's clause markup changed, fix this test", len(known), specPath)
	}

	f := load(t)
	cited := map[string]bool{}
	for _, v := range f.Vectors {
		if len(v.SpecClauses) == 0 {
			t.Errorf("vector %q cites no spec clause", v.Name)
			continue
		}
		for _, c := range v.SpecClauses {
			if !known[c] {
				t.Errorf("vector %q cites §%s, which does not exist in %s", v.Name, c, specPath)
			}
			cited[c] = true
		}
	}
	t.Logf("vectors cite %d distinct spec clauses out of %d in the spec", len(cited), len(known))
}

// TestNegativeVectorsPresent is done-condition 3: the fail-closed cases are
// named, present, and each really is a rejection (its expectation differs from
// what the attacker's event was trying to write).
func TestNegativeVectorsPresent(t *testing.T) {
	f := load(t)
	byName := map[string]foldvectors.Vector{}
	for _, v := range f.Vectors {
		byName[v.Name] = v
	}
	required := []string{
		"malformed_events_dropped",
		"forged_events_dropped",
		"untrusted_author_dropped",
		"trust_gate_disabled_admits_anyone",
		"board_pin_rejects_foreign_board_card",
		"grant_admits_and_revoke_is_prospective",
		"confidential_wrong_cek_placeholder",
		"confidential_no_decryptor_placeholder",
		"fold_gate_quarantines_plaintext_and_malformed",
		"labels_freeform_no_validation",
		"dep_unresolvable_and_cross_board_dropped_silently",
		"status_from_non_authority_ignored",
		// ready-ce8's four proven coverage holes. Named here so a regeneration
		// that silently drops one restores the hole loudly instead of quietly.
		"grant_cap_only_owner_grants_maintainer",
		"grant_cap_contributor_may_not_delegate",
		"grant_cap_owner_is_irrevocable",
		"grant_cap_peer_maintainer_protected",
		"revoke_boundary_excludes_the_revoke_instant",
		"grant_level_two_confers_status_authority",
	}
	for _, name := range required {
		if _, ok := byName[name]; !ok {
			t.Errorf("required negative vector %q is missing", name)
		}
	}

	// The wrong-CEK case must render the PLACEHOLDER, never an empty title and
	// never raw ciphertext — the specific fail-closed shape spec §11.7 requires.
	for _, name := range []string{"confidential_wrong_cek_placeholder", "confidential_no_decryptor_placeholder"} {
		v, ok := byName[name]
		if !ok {
			continue
		}
		if len(v.Expect.Items) != 1 {
			t.Fatalf("%s: expected exactly one item", name)
		}
		var item struct {
			Title       string   `json:"title"`
			Context     string   `json:"context"`
			Description string   `json:"description"`
			WaitingOn   string   `json:"waiting_on"`
			WaitingType string   `json:"waiting_type"`
			Status      string   `json:"status"`
			Labels      []string `json:"labels"`
		}
		if err := json.Unmarshal(v.Expect.Items[0], &item); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for field, got := range map[string]string{"title": item.Title, "context": item.Context, "description": item.Description} {
			if got != "[encrypted]" {
				t.Errorf("%s: %s = %q, want the placeholder \"[encrypted]\"", name, field, got)
			}
		}
		if item.WaitingOn != "" {
			t.Errorf("%s: waiting_on = %q, want it hidden", name, item.WaitingOn)
		}
		if item.WaitingType != "gate" || item.Status != "waiting" {
			t.Errorf("%s: clear routing fields must still render, got waiting_type=%q status=%q",
				name, item.WaitingType, item.Status)
		}
		if len(item.Labels) != 1 || len(item.Labels[0]) != 64 {
			t.Errorf("%s: labels must stay opaque 32-byte HMAC tokens, got %v", name, item.Labels)
		}
	}

	// The read-trust pair must DISAGREE. If enforcing the allowlist produced the
	// same projection as disabling it, untrusted_author_dropped would be vacuous.
	enforced, okA := byName["untrusted_author_dropped"]
	disabled, okB := byName["trust_gate_disabled_admits_anyone"]
	if okA && okB {
		if len(enforced.Events) != len(disabled.Events) {
			t.Error("the read-trust pair must fold the SAME event set")
		}
		if reflect.DeepEqual(normalizeAll(t, enforced.Expect.Items), normalizeAll(t, disabled.Expect.Items)) {
			t.Error("read-trust pair produces identical items: the rejection vector proves nothing")
		}
		if enforced.Options.Trusted == nil || disabled.Options.Trusted != nil {
			t.Error("read-trust pair must differ exactly in whether the allowlist is enforced")
		}
	}
}

// grantAuthorityVectors are ready-ce8's vectors: the ones whose whole purpose is
// to make a grant-authority mutation observable.
var grantAuthorityVectors = []string{
	"grant_cap_only_owner_grants_maintainer",
	"grant_cap_contributor_may_not_delegate",
	"grant_cap_owner_is_irrevocable",
	"grant_cap_peer_maintainer_protected",
	"revoke_boundary_excludes_the_revoke_instant",
	"grant_level_two_confers_status_authority",
}

// TestGrantAuthorityVectorsRunWithTheGatesEnabled is ready-ce8's structural
// guard, and it generalizes the defect the item was filed for.
//
// The four holes ready-3759's audit proved were not "the assertion is too weak".
// They were "the gate that enforces the property is INERT in the option shape
// every vector uses", so the property was never on the code path being asserted.
// The two options that switch grant authority on or off wholesale are:
//
//	options.trusted == null   -> §3.4 read-trust disabled entirely, so an
//	                             ignored grant and an honoured one both admit
//	                             their grantee and the escalation cap is
//	                             unobservable.
//	options.pinned_board == "" -> §12/§3.5/§6.2 all inert: ProjectItems derives
//	                             NO levels and NO until map at all, so the cap,
//	                             the revocation boundary and the grant-derived
//	                             maintainer fold are not merely unasserted, they
//	                             do not RUN.
//
// A future edit that flips either option on one of these vectors would leave it
// passing — green, and proving nothing, which is the exact failure mode this
// item exists to make impossible. So the option shape is asserted as part of the
// contract, not left as a property of whoever authored the fixture.
func TestGrantAuthorityVectorsRunWithTheGatesEnabled(t *testing.T) {
	f := load(t)
	byName := map[string]foldvectors.Vector{}
	for _, v := range f.Vectors {
		byName[v.Name] = v
	}
	for _, name := range grantAuthorityVectors {
		v, ok := byName[name]
		if !ok {
			t.Errorf("grant-authority vector %q is missing", name)
			continue
		}
		if v.Options.Trusted == nil {
			t.Errorf("%s: options.trusted is null, which DISABLES the §3.4 read-trust gate — "+
				"this vector's expectation would then hold with grant authority switched off", name)
		}
		if v.Options.PinnedBoard == "" {
			t.Errorf("%s: options.pinned_board is empty, so ProjectItems derives no levels and no "+
				"until map at all (§12, §3.5, §6.2 inert) — the property this vector pins would not run", name)
		}
	}
}

// ready882Clauses is the clause sweep ready-882 owns: three normative
// subsystems that had ZERO vectors when the corpus cited 82 of the spec's 134
// clauses — gate lifecycle TRANSITIONS (§9.4-§9.7 were covered, the transitions
// that produce and resolve a gate were not), the confidential-envelope EPOCH
// MODEL, and the two grant-replay clauses the escalation-cap vectors leave out.
//
// A subsystem with no vector is one where an independent client can disagree
// with `rd ready` and still pass conformance, which is the exact failure the
// epic's spec -> vectors -> client ordering exists to prevent. This test is what
// stops the hole from reopening: adding a clause here is cheap, and REMOVING one
// requires an entry in clauseExemptions with a stated reason.
//
// §12.3 / §12.5 / §12.6 are in the list but were landed by the sibling
// grant-authority item (cases_grantauthority.go) — they are asserted here so
// that a regeneration which drops those vectors is caught by this test too,
// not silently.
var ready882Clauses = []string{
	"9.1", "9.2", "9.3", "9.8",
	"11.10", "11.11", "11.12", "11.13", "11.14",
	"12.2", "12.3", "12.5", "12.6", "12.9",
}

// clauseExemptions records a ready882Clauses entry that deliberately has NO
// vector, with the reason. It is empty today: every clause in the sweep is
// cited. It exists because the honest alternative to a vector is a stated
// exemption, never a quiet deletion from the list above — a clause that leaves
// the list without landing here is a coverage regression disguised as a test
// edit.
var clauseExemptions = map[string]string{}

// clauseMutationGaps records a ready882Clauses entry that IS cited by a vector but
// for which no discriminating mutation exists — the implementation branch can be
// deleted outright and the whole corpus stays green — together with the proof that
// none can exist. A citation without a mutation receipt is the weakest kind of
// coverage, so the honest thing is to name it rather than let the clause count
// imply more than it earns.
//
// This is deliberately NOT the same thing as clauseExemptions ("no vector at
// all"), and a clause may not be in both.
var clauseMutationGaps = map[string]string{
	"9.8": "Deleting §9.8's clear-the-fields branch from pkg/sync/nostrproject.go leaves all " +
		"vectors green, and fold.ts's mirror is equally inert. PROVABLY so, not merely " +
		"unobserved: !declaresGate already means WaitingType/WaitingOn/Gate are empty (§9.4), " +
		"and WaitingSince/GateMsgID are assigned nowhere in either fold outside the gate loop " +
		"itself (§9.6) and come from no card or status tag (§5.1), while both folds rebuild the " +
		"item from the winning card (§22.2) so there is no prior revision to inherit from. No " +
		"input this format can express reaches the branch with a field to clear. The clause is " +
		"still normative and not vacuous — a client that MUTATES items across revisions instead " +
		"of rebuilding them carries a stale GateMsgID past an `rd approve` — so what is asserted " +
		"instead is the resulting INVARIANT, over the live fold, in " +
		"TestNoDeclaredGateMeansNoGateFields. See spec §9.8a.",
}

// TestReady882ClauseSweepIsCovered is the done condition of ready-882: each
// clause in the sweep is either cited by a vector or exempt with a reason.
func TestReady882ClauseSweepIsCovered(t *testing.T) {
	f := load(t)
	citedBy := map[string][]string{}
	for _, v := range f.Vectors {
		for _, c := range v.SpecClauses {
			citedBy[c] = append(citedBy[c], v.Name)
		}
	}
	inSweep := map[string]bool{}
	for _, clause := range ready882Clauses {
		inSweep[clause] = true
		vectors := citedBy[clause]
		reason, exempt := clauseExemptions[clause]
		gap, noMutation := clauseMutationGaps[clause]
		switch {
		case exempt && noMutation:
			t.Errorf("§%s is both exempt and recorded as a mutation gap — a clause with no vector "+
				"cannot also have a cited-but-unfalsifiable vector", clause)
		case len(vectors) > 0 && exempt:
			t.Errorf("§%s is both cited (%v) and exempt (%q) — drop the exemption", clause, vectors, reason)
		case len(vectors) > 0 && noMutation:
			t.Logf("§%s covered by %v, NO DISCRIMINATING MUTATION: %s", clause, vectors, gap)
		case len(vectors) > 0:
			t.Logf("§%s covered by %v", clause, vectors)
		case exempt:
			t.Logf("§%s EXEMPT: %s", clause, reason)
		default:
			t.Errorf("§%s has no vector and no exemption — the subsystem is unpinned again", clause)
		}
	}
	// A mutation gap is a statement ABOUT a cited clause in the sweep. Recording one
	// for a clause that is not cited at all would hide a coverage hole behind a
	// disclosure, so that combination is an error, not a note.
	for clause := range clauseMutationGaps {
		if !inSweep[clause] {
			t.Errorf("§%s is recorded as a mutation gap but is not in the sweep", clause)
		}
		if len(citedBy[clause]) == 0 {
			t.Errorf("§%s is recorded as a mutation gap but no vector cites it — that is an "+
				"exemption, not a gap", clause)
		}
	}
}

// TestNoDeclaredGateMeansNoGateFields is what §9.8 gets INSTEAD of a mutation
// receipt, and the difference is stated plainly because the honest failure mode
// here is a green test that implies more than it proves.
//
// §9.8 says an item with no declared gate has all four gate fields cleared. Its
// branch in pkg/sync/nostrproject.go can be deleted with the entire corpus staying
// green, and that is provable rather than a coverage accident — see
// clauseMutationGaps["9.8"] and spec §9.8a. So this is NOT a discriminating test:
// it is the INVARIANT §9.8 promises, asserted over the LIVE FOLD's output (not the
// hand-authored expectations) for every vector in the corpus, so that any future
// fold change or vector that produces a gate-less item carrying a WaitingSince or
// a GateMsgID is caught here even though deleting the clause's own branch is not.
//
// The TypeScript side inherits it: fold.vectors.test.ts asserts its projection
// equals the same committed expectations these items come from, so an item shape
// this test rejects cannot be a passing TS expectation either.
func TestNoDeclaredGateMeansNoGateFields(t *testing.T) {
	f := load(t)
	checkedGateless := 0
	checkedGated := 0
	for _, v := range f.Vectors {
		items, _, _, err := foldvectors.Run(v)
		if err != nil {
			t.Fatalf("%s: run: %v", v.Name, err)
		}
		for _, raw := range items {
			var it struct {
				ID           string `json:"id"`
				Gate         string `json:"gate"`
				WaitingType  string `json:"waiting_type"`
				WaitingOn    string `json:"waiting_on"`
				WaitingSince string `json:"waiting_since"`
				GateMsgID    string `json:"gate_msg_id"`
			}
			if err := json.Unmarshal(raw, &it); err != nil {
				t.Fatalf("%s: decode item: %v", v.Name, err)
			}
			if it.WaitingType != "" || it.WaitingOn != "" || it.Gate != "" {
				checkedGated++
				continue
			}
			checkedGateless++
			if it.WaitingSince != "" || it.GateMsgID != "" {
				t.Errorf("%s: item %s declares no gate (§9.4) yet carries waiting_since=%q "+
					"gate_msg_id=%q — §9.8 requires all four cleared", v.Name, it.ID,
					it.WaitingSince, it.GateMsgID)
			}
		}
	}
	// Anti-vacuity: the corpus must contain items on BOTH sides of §9.4's predicate,
	// or this test is asserting a property of the empty set.
	if checkedGateless == 0 {
		t.Fatal("no gate-less item in the whole corpus — §9.8's invariant is being asserted over nothing")
	}
	if checkedGated == 0 {
		t.Fatal("no gate-declaring item in the whole corpus — §9.6/§9.7 have nothing to distinguish §9.8 from")
	}
	t.Logf("§9.8 invariant holds over %d gate-less items (%d gate-declaring items exercise the other branch)",
		checkedGateless, checkedGated)
}

// keyringVectors are ready-882's epoch-model vectors: the ones whose whole point
// is that the key material is DERIVED from the vector's own grants.
var keyringVectors = []string{
	"keyring_epoch_zero_grant_yields_no_key",
	"keyring_epoch_zero_grant_yields_no_cutover",
	"keyring_retains_every_epoch_across_a_rotation",
	"keyring_cutover_is_the_earliest_owner_grant_whoever_it_names",
}

// TestKeyringVectorsDeriveRatherThanDeclareTheirKeys is the structural guard for
// those vectors, and it generalizes the defect that motivated the whole item.
//
// A confidential vector can express its key material two ways, and only ONE of
// them exercises the epoch model:
//
//	options.decryptor + options.encrypted_boards -> the keys and the cutover are
//	    HANDED to the fold as facts. §11.10-§11.14 do not run at all, so an
//	    expectation about them is vacuous — the same "the gate is inert in the
//	    option shape every vector uses" failure ready-ce8 found for grant
//	    authority.
//	options.keyring -> the client must DERIVE both from the vector's own
//	    owner-signed grants, which is the only shape under which "epochs are
//	    retained", "the cutover is the earliest grant" and "the current epoch is
//	    the highest held" are falsifiable.
//
// Rewriting one of these vectors into the declarative shape would leave it
// GREEN while testing none of what it claims, so the shape is part of the
// contract rather than a property of whoever authored the fixture.
func TestKeyringVectorsDeriveRatherThanDeclareTheirKeys(t *testing.T) {
	f := load(t)
	byName := map[string]foldvectors.Vector{}
	for _, v := range f.Vectors {
		byName[v.Name] = v
	}
	for _, name := range keyringVectors {
		v, ok := byName[name]
		if !ok {
			t.Errorf("keyring vector %q is missing", name)
			continue
		}
		if v.Options.Keyring == nil {
			t.Errorf("%s: options.keyring is null — the key material would be declared, not derived, "+
				"and §11.10-§11.14 would not run", name)
			continue
		}
		if v.Options.Decryptor != nil || v.Options.EncryptedBoards != nil {
			t.Errorf("%s: options.keyring is combined with the declarative key shape, so which one the "+
				"client used is unobservable", name)
		}
		if v.Expect.Keyring == nil {
			t.Errorf("%s: derives a keyring but asserts nothing about it", name)
		}
		// The grants the derivation must consume have to be IN the vector.
		grants := 0
		for _, e := range v.Events {
			if e != nil && e.Kind == rdsync.KindRoleGrant {
				grants++
			}
		}
		if grants == 0 {
			t.Errorf("%s: no kind-39301 grant in the event log, so the derived keyring is empty by "+
				"construction and proves nothing", name)
		}
	}
}

// TestGateLifecycleVectorsCoverAllThreeTransitions guards ready-882's other
// half. The gate clauses split into STANDING STATE (§9.4-§9.7: how a gate
// renders) and TRANSITIONS (§9.1 open, §9.2 approve, §9.3 reject): the corpus
// had the first group and none of the second, so a client could render a gate
// perfectly and still mishandle every event sequence that opens or resolves one.
// Approve and reject in particular are indistinguishable on the wire except by
// the status value and the dropped card tags (§22.4), so BOTH must be present —
// one alone cannot show the difference.
func TestGateLifecycleVectorsCoverAllThreeTransitions(t *testing.T) {
	f := load(t)
	byName := map[string]foldvectors.Vector{}
	for _, v := range f.Vectors {
		byName[v.Name] = v
	}
	transitions := map[string]string{
		"gate_open_projects_a_resolvable_gate":                      "9.1",
		"gate_approve_clears_every_gate_field":                      "9.2",
		"gate_approve_under_blocking_clears_the_gate_not_the_block": "9.2",
		"gate_reject_keeps_the_gate_open":                           "9.3",
	}
	for name, clause := range transitions {
		v, ok := byName[name]
		if !ok {
			t.Errorf("gate-transition vector %q (§%s) is missing", name, clause)
			continue
		}
		cited := false
		for _, c := range v.SpecClauses {
			if c == clause {
				cited = true
			}
		}
		if !cited {
			t.Errorf("%s no longer cites §%s", name, clause)
		}
		// Every one of these is an event PAIR (a card revision plus its status
		// event) applied to an item that already existed — a single event cannot
		// express a transition.
		if len(v.Events) < 3 {
			t.Errorf("%s: %d events — a gate transition is a card revision plus a status event on top "+
				"of a pre-existing item", name, len(v.Events))
		}
	}
	// The approve/reject pair must DISAGREE about the gate. If both projections
	// kept (or both cleared) the gate fields, neither vector would show that the
	// two resolutions differ.
	approve, okA := byName["gate_approve_clears_every_gate_field"]
	reject, okR := byName["gate_reject_keeps_the_gate_open"]
	if okA && okR {
		if gateMsgIDOf(t, approve) != "" {
			t.Error("gate_approve_clears_every_gate_field: gate_msg_id survived the approval")
		}
		if gateMsgIDOf(t, reject) == "" {
			t.Error("gate_reject_keeps_the_gate_open: the gate was cleared, so the reject reads as a resolution")
		}
		if len(approve.Expect.Views["gates"]) != 0 || len(reject.Expect.Views["gates"]) == 0 {
			t.Error("the approve/reject pair must differ in gates-view membership (§13.10)")
		}
	}
}

// gateMsgIDOf returns the single-item vector's projected gate_msg_id.
func gateMsgIDOf(t *testing.T, v foldvectors.Vector) string {
	t.Helper()
	if len(v.Expect.Items) != 1 {
		t.Fatalf("%s: expected exactly one item, got %d", v.Name, len(v.Expect.Items))
	}
	var item struct {
		GateMsgID string `json:"gate_msg_id"`
	}
	if err := json.Unmarshal(v.Expect.Items[0], &item); err != nil {
		t.Fatalf("%s: %v", v.Name, err)
	}
	return item.GateMsgID
}

// TestVectorEventsAreWellFormed sanity-checks the fixtures themselves: at least
// one vector carries a null event (spec §3.1) and every vector has events.
func TestVectorEventsAreWellFormed(t *testing.T) {
	f := load(t)
	nulls := 0
	for _, v := range f.Vectors {
		if len(v.Events) == 0 {
			t.Errorf("vector %q has no events", v.Name)
		}
		for _, e := range v.Events {
			if e == nil {
				nulls++
			}
		}
	}
	if nulls == 0 {
		t.Error("no vector exercises the nil-event guard (spec §3.1)")
	}
}

// TestTimestampEncodingPreservesArbitraryNanoseconds is a ready-414
// counterexample for a genuinely non-round int64 nanosecond value — one the
// live fold's `sec * int64(time.Second)` derivation (spec §4.6) never
// produces (it is not a multiple of 1e9), but which state.Item.CreatedAt's
// declared type (arbitrary int64 unix nanoseconds) does not forbid — is not
// exactly representable as an IEEE-754 double, the same type backing
// JavaScript's Number. A bare-number JSON encoding of this value is lossy at
// JSON.parse time on any JS/TS client; EncodeItem's decimal string is not.
// This is deliberately NOT a foldvectors.Vector: every vector's expect.items
// is checked against the live fold (build.go's add()), and the live fold's
// formula cannot produce a value that is not a multiple of 1e9.
//
// This is NOT the only counterexample in the suite, and is not needed to
// prove the encoding necessary on its own: item_timestamp_above_float64_safe_bound
// (internal/foldvectors/cases_encoding.go, committed in testdata/fold.vectors.json)
// IS a fold-checked vector, and its created_at IS a value the live fold
// actually produces (sec=4,611,686,019) that is also not exactly
// representable as a float64 — see
// TestTimestampCounterexampleVectorIsGenuinelyLossyInFloat64 below, which
// proves that against the committed file directly. This test's job is
// narrower: cover a value shape (not a multiple of 1e9) the live fold can
// never emit but the field's declared type permits.
func TestTimestampEncodingPreservesArbitraryNanoseconds(t *testing.T) {
	const nonRound int64 = 1700000000123456789 // NOT a multiple of 1e9

	// Ground truth: this specific value cannot round-trip through float64
	// exactly. If it did, it would be a useless fixture for this test — fail
	// loudly rather than silently prove nothing.
	if int64(float64(nonRound)) == nonRound {
		t.Fatalf("fixture %d round-trips through float64 exactly; this test needs a genuinely non-round value", nonRound)
	}

	it := &state.Item{ID: "ready-tsenc-proof", CreatedAt: nonRound, UpdatedAt: nonRound}
	blob, err := foldvectors.EncodeItem(it)
	if err != nil {
		t.Fatalf("EncodeItem: %v", err)
	}

	var decoded struct {
		CreatedAt string `json:"created_at"`
		UpdatedAt string `json:"updated_at"`
	}
	if err := json.Unmarshal(blob, &decoded); err != nil {
		t.Fatalf("decode %s: %v", blob, err)
	}

	want := strconv.FormatInt(nonRound, 10)
	if decoded.CreatedAt != want {
		t.Errorf("created_at = %q, want the exact decimal string %q — a bare number here is exactly the "+
			"defect ready-414 closes: it would render as %v after a JS JSON.parse, silently 21ns off",
			decoded.CreatedAt, want, float64(nonRound))
	}
	if decoded.UpdatedAt != want {
		t.Errorf("updated_at = %q, want %q", decoded.UpdatedAt, want)
	}

	got, err := strconv.ParseInt(decoded.CreatedAt, 10, 64)
	if err != nil || got != nonRound {
		t.Errorf("created_at %q does not parse back (via the string a client would use) to the original %d", decoded.CreatedAt, nonRound)
	}
}

// TestTimestampEncodingIsSelfDescribing is done-condition 3 (ready-414): the
// vector file must state its own timestamp encoding where a TS reader who
// never opens the Go source will find it, not only in Go doc comments.
func TestTimestampEncodingIsSelfDescribing(t *testing.T) {
	f := load(t)
	if f.TimestampEncoding == "" {
		t.Fatal("timestamp_encoding is empty — the vector file no longer documents, in the file itself, " +
			"that created_at/updated_at are decimal strings rather than bare numbers")
	}
	for _, must := range []string{"created_at", "updated_at", "BigInt"} {
		if !strings.Contains(f.TimestampEncoding, must) {
			t.Errorf("timestamp_encoding does not mention %q: %s", must, f.TimestampEncoding)
		}
	}
}

// vectorFileTimestampFieldPattern matches a JSON value that is a STRING
// containing only decimal digits (optionally a leading '-'): the RAW json
// token, quotes included, e.g. `"1700000000000000000"`. A bare number like
// `1700000000000000000` (no quotes) does not match this, and neither does a
// string containing anything other than digits (e.g. `"12.3"`, `"1e9"`,
// `"abc"`).
var vectorFileTimestampFieldPattern = regexp.MustCompile(`^"-?[0-9]+"$`)

// TestVectorFileTimestampsAreDecimalDigitStrings is the DATA half of
// ready-414's done-condition 3 (review finding: TestTimestampEncodingIsSelfDescribing,
// below, only substring-scans the file's PROSE note — a hand-typed sentence
// containing the words "created_at", "updated_at" and "BigInt" satisfies it
// with no relationship to what the data actually looks like). This test
// asserts the data itself: every expect.items[].created_at and .updated_at in
// the COMMITTED testdata/fold.vectors.json — for every vector, not only the
// ones ready-414 added — is a JSON string containing only decimal digits.
func TestVectorFileTimestampsAreDecimalDigitStrings(t *testing.T) {
	f := load(t)
	checked := 0
	for _, v := range f.Vectors {
		for i, raw := range v.Expect.Items {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(raw, &fields); err != nil {
				t.Fatalf("vector %q item %d: decode: %v", v.Name, i, err)
			}
			for _, key := range []string{"created_at", "updated_at"} {
				tok, ok := fields[key]
				if !ok {
					t.Errorf("vector %q item %d: field %q is missing", v.Name, i, key)
					continue
				}
				if !vectorFileTimestampFieldPattern.MatchString(string(tok)) {
					t.Errorf("vector %q item %d: field %q = %s is not a decimal-digit JSON string "+
						"(want a quoted string of digits, e.g. \"1700000000000000000\")",
						v.Name, i, key, tok)
					continue
				}
				checked++
			}
		}
	}
	if checked == 0 {
		t.Fatal("checked zero timestamp fields — the vector file looks empty or malformed")
	}
	t.Logf("verified %d created_at/updated_at fields are decimal-digit JSON strings", checked)
}

// TestTimestampCounterexampleVectorIsGenuinelyLossyInFloat64 proves, against
// the COMMITTED testdata/fold.vectors.json (not a fresh in-memory Build()),
// that item_timestamp_above_float64_safe_bound really is what ready-414's
// spec §4.8 fix claims: a value the LIVE FOLD produced (not a synthetic value
// invented only for a unit test) whose created_at does not survive an
// IEEE-754 double round-trip.
//
// This is the regeneration-proof half of the fix. build.go's add() only
// checks that the hand-authored expectation and the live-fold output AGREE —
// both go through EncodeItem, so a regression shared by both sides (e.g.
// EncodeItem reverting to bare numbers) would not be caught by add(), and
// running `go run ./internal/foldvectors/gen` after such a regression would
// happily launder it into a new "passing" committed file. This test instead
// decodes the COMMITTED file's raw JSON token directly, so it fails the
// moment created_at stops being a quoted decimal string, however the file
// came to be regenerated.
func TestTimestampCounterexampleVectorIsGenuinelyLossyInFloat64(t *testing.T) {
	const vectorName = "item_timestamp_above_float64_safe_bound"
	f := load(t)
	var v *foldvectors.Vector
	for i := range f.Vectors {
		if f.Vectors[i].Name == vectorName {
			v = &f.Vectors[i]
			break
		}
	}
	if v == nil {
		t.Fatalf("required vector %q is missing", vectorName)
	}
	if len(v.Expect.Items) != 1 {
		t.Fatalf("%s: expected exactly one item, got %d", vectorName, len(v.Expect.Items))
	}
	var item struct {
		CreatedAt string `json:"created_at"`
		UpdatedAt string `json:"updated_at"`
	}
	if err := json.Unmarshal(v.Expect.Items[0], &item); err != nil {
		t.Fatalf("%s: decode item: %v (created_at/updated_at must be JSON strings — see "+
			"TestVectorFileTimestampsAreDecimalDigitStrings)", vectorName, err)
	}
	created, err := strconv.ParseInt(item.CreatedAt, 10, 64)
	if err != nil {
		t.Fatalf("%s: created_at %q does not parse as an int64: %v", vectorName, item.CreatedAt, err)
	}
	if item.UpdatedAt != item.CreatedAt {
		t.Errorf("%s: updated_at %q != created_at %q", vectorName, item.UpdatedAt, item.CreatedAt)
	}

	// The actual claim: this real, fold-produced value is NOT exactly
	// representable as a float64. If it were, this vector would prove
	// nothing about the old bare-number encoding's defect — the whole point
	// of the spec §4.8 fix is that a fold-checked vector CAN be a genuine
	// counterexample (an earlier draft of the spec claimed it could not).
	lossy := int64(float64(created))
	if lossy == created {
		t.Fatalf("%s: created_at=%d round-trips through float64 exactly — this vector no longer "+
			"demonstrates the old-encoding defect it exists to pin; pick a value above spec §4.8's "+
			"bound (sec <= 4,611,686,018) with no factor of two", vectorName, created)
	}
	t.Logf("%s: created_at=%d, a bare-number float64 round-trip would have produced %d (off by %d)",
		vectorName, created, lossy, created-lossy)
}

func normalize(t *testing.T, raw json.RawMessage) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("normalize %s: %v", raw, err)
	}
	return v
}

func normalizeAll(t *testing.T, raws []json.RawMessage) []any {
	t.Helper()
	out := make([]any, 0, len(raws))
	for _, r := range raws {
		out = append(out, normalize(t, r))
	}
	return out
}

func itemIDs(blobs []json.RawMessage) []string {
	var out []string
	for _, b := range blobs {
		var m struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(b, &m)
		out = append(out, m.ID)
	}
	return out
}

func sorted(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func sameSet(a, b []string) bool {
	as, bs := sorted(a), sorted(b)
	if len(as) != len(bs) {
		return false
	}
	for i := range as {
		if as[i] != bs[i] {
			return false
		}
	}
	return true
}
