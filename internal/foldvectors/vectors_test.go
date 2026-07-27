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

// TestTimestampEncodingPreservesArbitraryNanoseconds is the ready-414
// counterexample: a genuinely non-round int64 nanosecond value — one the live
// fold's `sec * int64(time.Second)` derivation (spec §4.6) never produces, but
// which state.Item.CreatedAt's declared type (arbitrary int64 unix
// nanoseconds) does not forbid — is not exactly representable as an IEEE-754
// double, the same type backing JavaScript's Number. A bare-number JSON
// encoding of this value is lossy at JSON.parse time on any JS/TS client;
// EncodeItem's decimal string is not. This is deliberately NOT a
// foldvectors.Vector: every vector's expect.items is checked against the live
// fold (build.go's add()), and the live fold cannot produce this value — see
// cases_encoding.go's doc comment for why the closest fold-checked vector
// (item_timestamp_large_nontidy_value_encoded_as_string) cannot carry this
// proof itself.
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
