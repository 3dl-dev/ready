// Package writevectors defines the WRITE-side counterpart to internal/foldvectors
// (ready-bc5). Where foldvectors pins what rd's live FOLD projects from a given
// event set, writevectors pins what rd's live WRITER emits for a given mutation
// intent — the anti-drift mechanism for writes, exactly as
// testdata/fold.vectors.json is for reads.
//
// The normative contract is docs/design/board-fold-spec.md §16-27 (the WRITE
// model, ready-1bf). A vector is a pure data record:
//
//	an intent (an rd command name + a pre-existing item/board projection state)
//	  -> the signed nostr event(s) rd's REAL writer appends to the local
//	     authoritative log.
//
// The replay harness (cmd/rd/writevectors_test.go — it must live in cmd/rd
// because that is where the un-exported run*Nostr command bodies live, the
// same reason the fold vectors' harness lives in internal/foldvectors next to
// the real fold) drives every vector through the ACTUAL command functions
// (runClaimNostr, runCloseNostr, runGateNostr, ...), never a reimplementation,
// mirroring foldvectors' replay discipline: this file's vectors.go loads the
// COMMITTED testdata/write.vectors.json and the harness REPLAYS it — it never
// regenerates-and-blesses a fresh capture.
//
// WHAT IS COMPARED, WHAT IS NOT (spec-independent, restated in the file's own
// "compared" field so a client implementer never has to open this comment):
//   - Compared: event kind, the FULL tag set (every tag, order preserved and
//     significant wherever a tag repeats or the spec marks order normative —
//     see board-fold-spec.md §19.5), and the event Content's SHAPE.
//   - NOT compared: the event id and signature (both depend on the signing key
//     and are re-derived on every replay), and created_at (depends on wall-clock
//     and the vector's own key differs run to run in Go's replay — see below).
//   - A tag value that is signer-pubkey-dependent (the card's "a" board
//     coordinate, an assignee "p" tag naming the ACTOR) is NOT hardcoded as a
//     literal hex string: it is the placeholder token "{{owner}}", which the
//     harness substitutes with whatever pubkey THIS replay's fixed signing key
//     resolves to before comparing. This keeps the file portable across an
//     independent client's own key material without requiring this repo to
//     commit and manage a shared secret scalar.
package writevectors

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
)

// FormatVersion is bumped when the vector file's SHAPE changes (not when
// vectors are added).
const FormatVersion = 1

// File is the top-level write.vectors.json document.
type File struct {
	Version int `json:"version"`
	// Spec is the repo-relative path of the normative document every vector's
	// spec_clauses cite (docs/design/board-fold-spec.md, §16 onward).
	Spec string `json:"spec"`
	// Note is free text for a human opening the file cold.
	Note string `json:"note"`
	// Compared documents, IN this file, exactly which fields a conformance
	// check must compare and which it must ignore. See the package doc.
	Compared string `json:"compared"`
	// KnownGaps documents, in the file itself, every place the write side is
	// KNOWN to disagree with the fail-closed ideal a vector suite would
	// otherwise assert — each one cites the board-fold-spec.md open-question
	// clause that already records it (ready-1bf §27) rather than silently
	// asserting a behaviour the writer does not have. A vector suite that
	// hid these would be lying about coverage; one that invented enforcement
	// to make them pass would be changing writer behaviour, which this item
	// (ready-bc5) explicitly forbids.
	KnownGaps []string `json:"known_gaps"`
	Vectors   []Vector `json:"vectors"`
}

// ItemFixture is a (partial) rd item projection state: either the state a
// vector's intent starts from (Vector.Seed / Vector.ExtraSeeds) or the state
// the round-trip check expects the produced event(s) to fold back into
// (Vector.PostFold). Empty/zero fields are "don't care" for PostFold and
// "leave at the Go zero value" for Seed.
type ItemFixture struct {
	ID          string   `json:"id"`
	Title       string   `json:"title,omitempty"`
	Context     string   `json:"context,omitempty"`
	Type        string   `json:"type,omitempty"`
	Priority    string   `json:"priority,omitempty"`
	Status      string   `json:"status,omitempty"`
	By          string   `json:"by,omitempty"`
	For         string   `json:"for,omitempty"`
	Level       string   `json:"level,omitempty"`
	ParentID    string   `json:"parent_id,omitempty"`
	ETA         string   `json:"eta,omitempty"`
	Due         string   `json:"due,omitempty"`
	Labels      []string `json:"labels,omitempty"`
	BlockedBy   []string `json:"blocked_by,omitempty"`
	Gate        string   `json:"gate,omitempty"`
	WaitingType string   `json:"waiting_type,omitempty"`
	WaitingOn   string   `json:"waiting_on,omitempty"`
}

// Vector is one write-conformance case.
type Vector struct {
	Name string `json:"name"`
	// SpecClauses lists the board-fold-spec clause ids (e.g. "20.1") this case
	// pins down. Every vector MUST cite at least one, and every cited clause
	// MUST exist in the spec — both enforced by TestEveryWriteVectorCitesALiveSpecClause.
	SpecClauses []string `json:"spec_clauses"`
	// Note explains what the case proves, including any behaviour the spec
	// records as an OPEN QUESTION (board-fold-spec.md §27) — such a vector pins
	// today's behaviour, it does not bless it.
	Note string `json:"note"`

	// Op names the rd command whose LIVE body the harness invokes. See
	// opTable in cmd/rd/writevectors_test.go for the exact run*Nostr function
	// and argument mapping each op name dispatches to.
	Op string `json:"op"`

	// Seed is the item the projection already contains before Op runs (the
	// read-modify-write starting point every live mutation reads from, spec
	// §16.3). Published through the SAME real writer (publishItemFullCreateNostr)
	// the create op itself uses — never a synthetic/injected event. Omit for
	// an op (like "create") that starts from nothing.
	Seed *ItemFixture `json:"seed,omitempty"`
	// ExtraSeeds are OTHER items the intent's op needs to already exist (e.g.
	// a blocker for a dep_add vector, or a second in-progress item for a
	// same-second monotonicity pair). Published before Seed, in order.
	ExtraSeeds []ItemFixture `json:"extra_seeds,omitempty"`

	// Args are the op's own string parameters. Which keys a given op reads is
	// documented per-op in cmd/rd/writevectors_test.go's opTable (e.g. claim
	// reads "reason"; delegate reads "to" and "reason"; gate_open reads
	// "gate_type" and "description"; label_add/label_remove read "label";
	// dep_add reads "blocker_id" naming an ExtraSeeds/Seed item id).
	Args map[string]string `json:"args,omitempty"`

	// ExpectError, when true, means the op MUST return a non-nil error
	// containing ErrorContains, and MUST NOT append any new event to the log —
	// a fail-closed negative vector. Events MUST be empty when this is true.
	ExpectError   bool   `json:"expect_error,omitempty"`
	ErrorContains string `json:"error_contains,omitempty"`

	// Events is the exact, ORDERED list of events the op must append to the
	// local authoritative log beyond whatever the seeds already put there.
	// Order is normative: a live publish call appends events in one fixed
	// sequence (e.g. card THEN status), and an independent client's replay
	// must match it, not merely produce the same set.
	Events []EventExpect `json:"events,omitempty"`

	// PostFold, when non-nil, is the round-trip check (ready-bc5 done
	// condition 4): every event this vector's SEEDS + Op produced, replayed
	// through pkg/sync.ProjectItems, must fold the vector's item id to a
	// projection matching these fields exactly. nil skips the round-trip
	// check (used only for ExpectError vectors and the couple of cases the
	// harness special-cases — see its doc comment).
	PostFold *ItemFixture `json:"post_fold,omitempty"`
}

// EventExpect pins one appended event.
type EventExpect struct {
	Kind int        `json:"kind"`
	Tags [][]string `json:"tags"`
	// Content describes event.Content's SHAPE — see ContentShape. Plaintext
	// mode content is fully deterministic (a literal reason string, or the
	// item's context, or empty) so it is compared exactly; this suite runs
	// every vector on a PUBLIC (plaintext) board — see the file's KnownGaps
	// for why the confidential default is a separate, documented gap rather
	// than silently covered here.
	Content ContentShape `json:"content"`
}

// ContentShape is a discriminated description of an event's Content field.
type ContentShape struct {
	// Mode is "empty" (Content must equal "") or "literal" (Content must equal
	// Value exactly).
	Mode  string `json:"mode"`
	Value string `json:"value,omitempty"`
}

// Load reads a vector file from disk and rejects an unsupported format
// version or an unknown field (a truncated or hand-edited file fails loudly
// rather than silently dropping data).
func Load(path string) (*File, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f File
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	if f.Version != FormatVersion {
		return nil, fmt.Errorf("%s: unsupported format version %d (want %d)", path, f.Version, FormatVersion)
	}
	return &f, nil
}
