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
//   - View expectations are SETS, never ordered lists. rd's rendered order is not
//     a total order (spec §15.7 — `sortByPriorityETA` ties and sorts an
//     unstable map iteration), so asserting order would be asserting a flake.
//     Ids inside a view are emitted sorted only so the file diffs cleanly.
package foldvectors

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/state"
	rdsync "github.com/3dl-dev/ready/pkg/sync"
	"github.com/3dl-dev/ready/pkg/views"
)

// FormatVersion is bumped when the vector file's SHAPE changes (not when
// vectors are added). An independent client should refuse a version it does not
// understand rather than silently mis-read a field.
const FormatVersion = 1

// File is the top-level fold.vectors.json document.
type File struct {
	Version int `json:"version"`
	// Spec is the repo-relative path of the normative document every vector's
	// spec_clauses refer to.
	Spec string `json:"spec"`
	// Note is free text for a human opening the file cold.
	Note string `json:"note"`
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
	// view (emitted sorted; compared as a set — see the package doc).
	Views map[string][]string `json:"views"`
	// LabelViews maps a label atom to the SET of item ids carrying it
	// (views.LabelFilter, spec §13.12). Empty when the case does not exercise it.
	LabelViews map[string][]string `json:"label_views,omitempty"`
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
	opts, err := v.Options.projectOptions()
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
		blob, mErr := json.Marshal(it)
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
// membership is a SET (spec §15.7 — rendered order is not a total order).
func idsOf(in []*state.Item) []string {
	out := make([]string, 0, len(in))
	for _, it := range in {
		out = append(out, it.ID)
	}
	sort.Strings(out)
	return out
}

// projectOptions materializes the data-form options into sync.ProjectOptions.
// A nil Trusted stays nil (gate disabled); an empty Maintainers list stays nil
// so the "no explicit maintainers" case is the production wiring exactly.
func (o Options) projectOptions() (rdsync.ProjectOptions, error) {
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
