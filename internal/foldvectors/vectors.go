// Package foldvectors defines the LANGUAGE-NEUTRAL conformance vector format for
// rd's board fold and the harness that replays a vector through rd's OWN fold.
//
// The normative contract is docs/design/board-fold-spec.md. A vector is a pure
// data record:
//
//	signed nostr events + fold options  ->  projected items + view membership
//
// The pipeline a vector exercises is the LIVE one (spec §1.1):
//
//	events -> sync.ProjectItems(events, ProjectOptions{...}) -> map[itemID]*state.Item
//	       -> pkg/views predicates -> per-view item-id SETS
//
// NOT pkg/state.DeriveAll, which is the dead campfire-era fold (spec §1.2, §14).
//
// Two hard rules keep the file usable by an independent (e.g. TypeScript) client:
//
//   - The file is DATA. Nothing in it is Go-specific: every value is a JSON
//     string / number / bool / array / object. `null` appears only where it is
//     semantically meaningful (a null event exercises spec §3.1; a null
//     `options.trusted` means "read-trust gate disabled", spec §3.4).
//   - View expectations are SETS, never ordered lists — but NOT for the reason
//     this comment used to give. The old reason ("rd's rendered order is not a
//     total order, so asserting order would be asserting a flake") is FALSE as
//     of ready-f5f: `sortByPriorityETA` is now a strict total order over
//     (priority, ETA, ID) and spec §15.7 explicitly lifts the no-ordering
//     caveat. The reason today is narrower and mechanical: this harness applies
//     the view predicates to items sorted BY ID (see Run), not to the slice
//     `sortByPriorityETA` would produce, and that sort lives in package main
//     (`cmd/rd/ready.go`) where neither this package nor an independent client
//     can reach it. Making view ORDER part of the cross-client contract means
//     first making the sort importable and holding every client to it — a real
//     change to what the board must implement, tracked separately, not
//     something to infer from this comment.
//   - Item CONTENT arrays are a different matter and ARE order-asserted:
//     `blocks` / `blocked_by` are compared field-for-field in the order the
//     fold emits them, which spec §8.1a fixes as ascending. That is pinned by
//     `dep_edge_arrays_are_sorted_not_visit_order`; before it, every vector
//     carried at most ONE entry per edge array, so the contract could not tell
//     a sorted implementation from an unsorted one.
package foldvectors

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"

	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/state"
	rdsync "github.com/3dl-dev/ready/pkg/sync"
	"github.com/3dl-dev/ready/pkg/views"
)

// FormatVersion is bumped when the vector file's SHAPE changes (not when
// vectors are added). An independent client should refuse a version it does not
// understand rather than silently mis-read a field.
//
// Bumped 1 -> 2 by ready-414: expect.items[].created_at / updated_at changed
// from bare JSON numbers to decimal STRINGS (see EncodeItem, TimestampEncoding
// and spec §4.8). A client still reading version 1 was silently losing
// nanosecond precision on JSON.parse for any int64 value not shaped like
// today's fold output; the version bump forces a client to notice the shape
// changed instead of mis-parsing it.
//
// Bumped 2 -> 3 by ready-882: `options.keyring` and `expect.keyring` were added
// so the CONFIDENTIAL-ENVELOPE EPOCH MODEL (spec §11.10-§11.14) is inside the
// vector contract at all. Until this bump every confidential vector handed the
// fold a keyring as literal DATA (`options.decryptor`, `options.encrypted_boards`),
// which skips the derivation entirely: which epochs a reader holds, when the
// board went confidential, and which epoch a write seals under were unasserted,
// so a client could derive all three wrongly and still pass. A vector carrying
// `options.keyring` requires the client to DERIVE that key material from the
// vector's own owner-signed kind-39301 grants; a client that ignores the field
// silently folds with no keys and no cutover, which is why the version has to
// move rather than the field simply appearing.
const FormatVersion = 3

// tsFields names the two Item JSON fields that carry arbitrary int64
// unix-nanosecond timestamps. See EncodeItem.
var tsFields = [2]string{"created_at", "updated_at"}

// EncodeItem marshals a projected item into the JSON that lands in a vector's
// expect.items, re-encoding its two int64 unix-nanosecond fields (created_at,
// updated_at) as decimal STRINGS instead of bare numbers.
//
// This operates on the DECODED top-level JSON object (a map[string]RawMessage),
// not on the marshaled bytes: an earlier version of this function used a regex
// over the marshaled bytes, which would have rewritten the "created_at":N byte
// pattern WHEREVER it occurred, including inside a string field's content, had
// such content ever produced that exact unescaped byte sequence. It cannot, in
// fact, for state.Item's current shape -- encoding/json always backslash-escapes
// a literal `"` inside string content, and a JSON value's closing delimiter is
// always followed by `,` or `}`, never `:` (only a KEY position is), so no
// user-controlled string field can ever produce an unescaped
// `"created_at":<digits>` byte run at a non-key position. But that safety was an
// accident of the byte-level approach happening not to be exploitable against
// today's fixed struct, not a property the byte-level approach guaranteed -- a
// future field of type json.RawMessage or map[string]any could reintroduce
// exactly this risk. Decoding to {string: RawMessage} and touching only the two
// named top-level keys removes the dependency on that accident entirely: it can
// only ever affect the two fields it names, regardless of what any other field
// contains. See item_context_contains_timestamp_byte_pattern in
// cases_encoding.go, which pins a Context value containing that literal byte
// pattern and confirms it is not touched.
//
// state.Item's own JSON tags are untouched by this -- rd's real JSON output
// (CLI, wire) is not part of this change -- only the copy that lands in
// testdata/fold.vectors.json is reshaped. That is deliberate: the fold's
// derivation of these two fields (event created_at, unix SECONDS, times
// int64(time.Second), spec §4.6) produces an EXACT multiple of 1e9, but
// whether that value survives an IEEE-754 double round-trip depends on its
// magnitude -- see spec §4.8 for the derivation of the real bound
// (sec <= 4,611,686,018 is guaranteed exact; above that it depends on sec's
// own factors of two) and item_timestamp_above_float64_safe_bound in
// cases_encoding.go for a live-fold-produced value that is provably lossy.
// The FIELD'S TYPE makes no promise either way, and JavaScript's Number
// cannot represent an arbitrary 64-bit integer exactly
// (Number.MAX_SAFE_INTEGER = 2^53-1 = 9007199254740991). A vector file that
// hands a client a bare number here is correct only for values below the
// bound; the string encoding removes the dependency on magnitude entirely.
// See also TestTimestampEncodingPreservesArbitraryNanoseconds for a
// synthetic (non-fold-producible) value that concretely fails under the old
// (bare-number) encoding.
//
// Both places that turn a *state.Item into vector JSON -- the hand-authored
// expectation in build.go's itemsJSON, and the live-fold comparison in Run()
// below -- call this one function, so a case added after this fix (ready-ce8,
// ready-882) gets the encoding for free and authoring/verification can never
// silently disagree about it.
func EncodeItem(it *state.Item) (json.RawMessage, error) {
	blob, err := json.Marshal(it)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(blob, &fields); err != nil {
		return nil, fmt.Errorf("EncodeItem: decode marshaled item: %w", err)
	}
	for _, key := range tsFields {
		raw, ok := fields[key]
		if !ok {
			continue
		}
		n, err := strconv.ParseInt(string(raw), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("EncodeItem: field %q = %s is not a bare integer", key, raw)
		}
		strEncoded, err := json.Marshal(strconv.FormatInt(n, 10))
		if err != nil {
			return nil, err
		}
		fields[key] = strEncoded
	}
	return json.Marshal(fields)
}

// File is the top-level fold.vectors.json document.
type File struct {
	Version int `json:"version"`
	// Spec is the repo-relative path of the normative document every vector's
	// spec_clauses refer to.
	Spec string `json:"spec"`
	// Note is free text for a human opening the file cold.
	Note string `json:"note"`
	// TimestampEncoding documents, IN this file (not only in Go comments — see
	// EncodeItem), how expect.items[].created_at / updated_at are encoded. A
	// client MUST read this before assuming those two fields are ordinary JSON
	// numbers: they are decimal STRINGS, parse them with BigInt(), never
	// Number() or JSON's own number parser. See spec §4.8.
	TimestampEncoding string `json:"timestamp_encoding"`
	// Keys are the fixture identities, by role name. Secrets are published on
	// purpose: an independent client must be able to RE-SIGN the fixtures (and to
	// prove to itself that the forged vectors really are forged).
	Keys []Key `json:"keys"`
	// Vectors is the ordered vector list. Order is presentational only.
	Vectors []Vector `json:"vectors"`
}

// Key is a fixture identity. Secret is the 32-byte lowercase-hex secp256k1
// scalar; Pubkey is the x-only 32-byte lowercase-hex public key.
type Key struct {
	Name   string `json:"name"`
	Secret string `json:"secret"`
	Pubkey string `json:"pubkey"`
}

// Vector is one conformance case.
type Vector struct {
	Name string `json:"name"`
	// SpecClauses lists the board-fold-spec clause ids this case pins down, e.g.
	// "4.1". Every vector MUST cite at least one, and every cited clause MUST
	// exist in the spec — both are enforced by the test.
	SpecClauses []string `json:"spec_clauses"`
	// Note explains what the case proves, including any behaviour that the spec
	// records as an OPEN QUESTION (§15) — such a vector pins today's behaviour,
	// it does not bless it.
	Note string `json:"note"`
	// Options are the fold inputs other than the events themselves.
	Options Options `json:"options"`
	// Identity is the caller identity the identity-scoped view predicates
	// (delegated, my-work) are evaluated for. "" means "no identity", which
	// those two predicates treat as "matches nothing" (spec §13.9).
	Identity string `json:"identity"`
	// Events is the signed event log, in append order. A null element is a
	// deliberate nil-event case (spec §3.1).
	Events []*nostr.Event `json:"events"`
	Expect Expect         `json:"expect"`
}

// Options mirrors sync.ProjectOptions as data.
type Options struct {
	// Trusted is the read-trust allowlist. NULL disables the gate entirely
	// (spec §3.4); a (possibly empty) array ENFORCES it.
	Trusted *[]string `json:"trusted"`
	// Maintainers is the explicit supplementary status-authority set (spec §6.3).
	Maintainers []string `json:"maintainers"`
	// PinnedBoard is "30301:<owner>:<boardD>" or "" (gates inert, spec §3.8).
	PinnedBoard string `json:"pinned_board"`
	// Decryptor, when non-null, is the set of content-encryption keys the reader
	// holds (spec §11.6). Null models a non-granted reader.
	Decryptor *DecryptorSpec `json:"decryptor"`
	// EncryptedBoards, when non-null, drives the fail-closed fold gate
	// (spec §3.9, §11.3). Null leaves every board plaintext-legal.
	EncryptedBoards *EncryptedBoardsSpec `json:"encrypted_boards"`
	// Keyring, when non-null, replaces BOTH of the two fields above with key
	// material the client must DERIVE from this vector's own events
	// (spec §11.10-§11.14). See KeyringSpec. It is an error for a vector to set
	// Keyring together with Decryptor or EncryptedBoards: the two express the
	// same two fold inputs and a vector that set both would be asserting nothing
	// about which one the client used.
	Keyring *KeyringSpec `json:"keyring"`
}

// KeyringSpec asks a conforming implementation to DERIVE the reader's
// confidential-board key material from the vector's own event log instead of
// being handed it (`DeriveBoardKeyring`, `pkg/sync/keydist.go`, spec §11.12),
// and to wire the result into BOTH fold inputs — the decryptor (spec §11.6) and
// the encrypted-board set (spec §3.9, §11.3) — exactly as rd does
// (`cmd/rd/nostr.go:969-976`).
//
// This is the ONLY option shape under which the epoch model is observable at
// all. `options.decryptor` states which (coordinate, epoch) keys the reader
// holds as a fact; `options.keyring` makes it a CONSEQUENCE of the owner-signed
// grants in `events`, so §11.10 (epoch >= 1), §11.12 (all epochs retained, not
// latest-wins), §11.13 (cutover = earliest owner CEK grant, whoever it is
// addressed to) and §11.14 (current epoch = highest held) each become
// falsifiable.
type KeyringSpec struct {
	// ReaderSecret is the 32-byte lowercase-hex secp256k1 scalar of the reader
	// the keyring is derived FOR. A secret, not a pubkey: unwrapping a grant's
	// NIP-44 wrap is an ECDH operation and is itself one of the admission checks
	// (spec §11.12 — a wrap addressed to somebody else does not open). Fixture
	// secrets are published in this file's `keys` on purpose.
	ReaderSecret string `json:"reader_secret"`
	// BoardAuthor and BoardD name the board whose key material is derived. Both
	// are part of the check: a grant must bind to this exact coordinate
	// (spec §11.12, §12.2).
	BoardAuthor string `json:"board_author"`
	BoardD      string `json:"board_d"`
}

// DecryptorSpec is the reader's keyring as data.
type DecryptorSpec struct {
	Keys []CEKEntry `json:"keys"`
}

// CEKEntry is one (board coordinate, epoch) -> 32-byte CEK binding.
type CEKEntry struct {
	BoardCoord string `json:"board_coord"`
	Epoch      int    `json:"epoch"`
	CEKHex     string `json:"cek_hex"`
}

// EncryptedBoardsSpec marks boards confidential and carries their cutover.
type EncryptedBoardsSpec struct {
	Boards []BoardCutover `json:"boards"`
}

// BoardCutover is one confidential board and its first-epoch cutover
// (unix SECONDS, spec §11.13).
type BoardCutover struct {
	BoardCoord string `json:"board_coord"`
	Cutover    int64  `json:"cutover"`
}

// Expect is the vector's expected fold output.
type Expect struct {
	// Items is every projected item, serialized exactly as state.Item's JSON
	// tags render it, sorted by id. An implementation conforms when its item
	// JSON is FIELD-FOR-FIELD equal to this (extra or missing fields both fail).
	Items []json.RawMessage `json:"items"`
	// Views maps each of the eight view names to the SET of item ids in that
	// view (emitted sorted; compared as a set — see the package doc for why the
	// set comparison is a limit of this harness, not a statement that rd's
	// order is nondeterministic).
	Views map[string][]string `json:"views"`
	// LabelViews maps a label atom to the SET of item ids carrying it
	// (views.LabelFilter, spec §13.12). Empty when the case does not exercise it.
	LabelViews map[string][]string `json:"label_views,omitempty"`
	// Keyring is the key material the client must have DERIVED from this
	// vector's grants, asserted directly. Present only on a vector that sets
	// options.keyring. See KeyringFacts.
	Keyring *KeyringFacts `json:"keyring,omitempty"`
}

// KeyringFacts is the derived-keyring expectation: the three answers
// DeriveBoardKeyring produces that the projected items cannot fully show.
//
// Cutover/Confidential DO also show up in the items (a post-cutover plaintext
// card is quarantined, spec §11.3, while a pre-cutover one is grandfathered,
// §11.4), and a vector that asserts them here SHOULD also carry that item-level
// consequence — the direct assertion says WHICH instant, the items say the
// instant is load-bearing. CurrentEpoch has no item-level consequence at all: it
// is the epoch a WRITE seals under (spec §11.14), so a read-only conformance run
// is the only place a client can be held to it before it starts publishing.
type KeyringFacts struct {
	// BoardCoord is the "30301:<owner>:<d>" the three answers below are about.
	BoardCoord string `json:"board_coord"`
	// Confidential is `Cutover(coord)`'s ok — "at least one owner-signed
	// CEK-bearing grant reached this reader" (spec §11.13). False means the fold
	// gate is INERT for this board, not that the board is known-plaintext.
	Confidential bool `json:"confidential"`
	// Cutover is the derived cutover in unix SECONDS, 0 when Confidential is
	// false (spec §11.13).
	Cutover int64 `json:"cutover"`
	// CurrentEpoch is the HIGHEST epoch this reader holds for the board, 0 when
	// it holds none (spec §11.14).
	CurrentEpoch int `json:"current_epoch"`
}

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

// Load reads a vector file from disk.
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

// Run replays one vector through rd's live fold and returns the projected items
// (JSON-normalized, sorted by id), the view membership sets, and the label-view
// sets for whatever atoms the vector asks about.
func Run(v Vector) (items []json.RawMessage, viewSets map[string][]string, labelSets map[string][]string, err error) {
	opts, err := v.Options.projectOptions(v.Events)
	if err != nil {
		return nil, nil, nil, err
	}
	projected := rdsync.ProjectItems(v.Events, opts)

	ids := make([]string, 0, len(projected))
	for id := range projected {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	ordered := make([]*state.Item, 0, len(ids))
	for _, id := range ids {
		ordered = append(ordered, projected[id])
	}

	items = make([]json.RawMessage, 0, len(ordered))
	for _, it := range ordered {
		blob, mErr := EncodeItem(it)
		if mErr != nil {
			return nil, nil, nil, mErr
		}
		items = append(items, blob)
	}

	viewSets = map[string][]string{}
	for _, name := range views.AllNames() {
		f := views.Named(name, v.Identity)
		if f == nil {
			return nil, nil, nil, fmt.Errorf("view %q has no filter", name)
		}
		viewSets[name] = idsOf(views.Apply(ordered, f))
	}

	if len(v.Expect.LabelViews) > 0 {
		labelSets = map[string][]string{}
		for atom := range v.Expect.LabelViews {
			labelSets[atom] = idsOf(views.Apply(ordered, views.LabelFilter(atom)))
		}
	}
	return items, viewSets, labelSets, nil
}

// idsOf returns the sorted item ids of a filtered slice. Sorted because view
// membership is compared as a SET — see the package doc for why that is a
// property of this harness (it never runs `sortByPriorityETA`) and no longer a
// claim that rd's rendered order is nondeterministic, which §15.7 retired.
func idsOf(in []*state.Item) []string {
	out := make([]string, 0, len(in))
	for _, it := range in {
		out = append(out, it.ID)
	}
	sort.Strings(out)
	return out
}

// deriveKeyring builds the reader keyring from the vector's OWN events, exactly
// as rd does before folding (`boardReadKeyring` -> `DeriveBoardKeyring`,
// `cmd/rd/nostr.go:969`). Returns nil when the vector does not use the derived
// form.
//
// It is the ONE derivation both consumers go through — the fold inputs
// (projectOptions) and the asserted facts (KeyringFactsFor) — so a vector's
// expect.keyring can never describe a keyring different from the one its items
// were folded with.
func (o Options) deriveKeyring(events []*nostr.Event) (*rdsync.BoardKeyring, error) {
	if o.Keyring == nil {
		return nil, nil
	}
	reader, err := nostr.KeyFromHex(o.Keyring.ReaderSecret)
	if err != nil {
		return nil, fmt.Errorf("keyring.reader_secret: %w", err)
	}
	return rdsync.DeriveBoardKeyring(events, reader, o.Keyring.BoardAuthor, o.Keyring.BoardD), nil
}

// KeyringFactsFor derives the vector's keyring and reports the facts
// expect.keyring asserts (spec §11.13, §11.14). It returns nil when the vector
// does not use the derived-keyring form.
func KeyringFactsFor(v Vector) (*KeyringFacts, error) {
	kr, err := v.Options.deriveKeyring(v.Events)
	if err != nil || kr == nil {
		return nil, err
	}
	coord := rdsync.BoardCoord(v.Options.Keyring.BoardAuthor, v.Options.Keyring.BoardD)
	cutover, confidential := kr.Cutover(coord)
	if !confidential {
		// Cutover's int64 is meaningless when ok is false; pin the zero so the
		// file never suggests a client should read it.
		cutover = 0
	}
	epoch, _, held := kr.CurrentEpoch(coord)
	if !held {
		epoch = 0
	}
	return &KeyringFacts{
		BoardCoord:   coord,
		Confidential: confidential,
		Cutover:      cutover,
		CurrentEpoch: epoch,
	}, nil
}

// projectOptions materializes the data-form options into sync.ProjectOptions.
// A nil Trusted stays nil (gate disabled); an empty Maintainers list stays nil
// so the "no explicit maintainers" case is the production wiring exactly.
//
// events is needed only by the derived-keyring form (Options.Keyring): that
// keyring is a function of the SAME signed log the fold then replays, which is
// the whole point — the key material is not an out-of-band fact.
func (o Options) projectOptions(events []*nostr.Event) (rdsync.ProjectOptions, error) {
	var opts rdsync.ProjectOptions
	if o.Trusted != nil {
		set := map[string]bool{}
		for _, pk := range *o.Trusted {
			set[pk] = true
		}
		opts.Trusted = set
	}
	if len(o.Maintainers) > 0 {
		set := map[string]bool{}
		for _, pk := range o.Maintainers {
			set[pk] = true
		}
		opts.Maintainers = set
	}
	opts.PinnedBoard = o.PinnedBoard
	if o.Decryptor != nil {
		d := mapDecryptor{keys: map[string][32]byte{}}
		for _, k := range o.Decryptor.Keys {
			raw, err := hex.DecodeString(k.CEKHex)
			if err != nil {
				return opts, fmt.Errorf("decode cek_hex: %w", err)
			}
			if len(raw) != 32 {
				return opts, fmt.Errorf("cek_hex must be 32 bytes, got %d", len(raw))
			}
			var cek [32]byte
			copy(cek[:], raw)
			d.keys[cekKey(k.BoardCoord, k.Epoch)] = cek
		}
		opts.Decryptor = d
	}
	if o.EncryptedBoards != nil {
		b := mapBoards{cutovers: map[string]int64{}}
		for _, e := range o.EncryptedBoards.Boards {
			b.cutovers[e.BoardCoord] = e.Cutover
		}
		opts.EncryptedBoards = b
	}
	if o.Keyring != nil {
		if o.Decryptor != nil || o.EncryptedBoards != nil {
			return opts, errors.New("options.keyring cannot be combined with options.decryptor / options.encrypted_boards: they set the same two fold inputs")
		}
		kr, err := o.deriveKeyring(events)
		if err != nil {
			return opts, err
		}
		// ONE object in BOTH slots, as production wires it
		// (cmd/rd/nostr.go:969-976): the keys a reader holds and the boards it
		// knows are confidential come from the same derivation, so they cannot
		// disagree.
		opts.Decryptor = kr
		opts.EncryptedBoards = kr
	}
	return opts, nil
}

func cekKey(coord string, epoch int) string { return fmt.Sprintf("%s|%d", coord, epoch) }

// mapDecryptor is the data-driven sync.BoardDecryptor: it holds exactly the keys
// the vector says the reader holds, so a "wrong CEK" case is expressed by
// listing a DIFFERENT key for the same (coordinate, epoch) slot.
type mapDecryptor struct{ keys map[string][32]byte }

func (d mapDecryptor) CEK(boardCoord string, epoch int) ([32]byte, bool) {
	cek, ok := d.keys[cekKey(boardCoord, epoch)]
	return cek, ok
}

// mapBoards is the data-driven sync.EncryptedBoardSet.
type mapBoards struct{ cutovers map[string]int64 }

func (b mapBoards) Cutover(boardCoord string) (int64, bool) {
	c, ok := b.cutovers[boardCoord]
	return c, ok
}
