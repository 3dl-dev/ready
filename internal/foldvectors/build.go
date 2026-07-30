package foldvectors

// build.go CONSTRUCTS the conformance vector set: the fixture keys, the signed
// events of each case, and — crucially — the EXPECTED output, which is written
// out by hand from docs/design/board-fold-spec.md rather than dumped from a run
// of the implementation.
//
// That distinction is the whole point of the suite. A generator that folds the
// events and enshrines whatever came out passes forever and encodes today's bugs
// as the contract. Here every expected *state.Item literal and every expected
// view set is authored against a spec clause, and Build() then CHECKS the
// hand-authored expectation against the live fold and fails loudly on a
// disagreement — so a mismatch is investigated (either the reading was wrong, or
// a real divergence was found and gets filed) instead of being papered over.
//
// The only values not hand-authored are IDENTITIES that are content hashes:
// an event id (Item.MsgID, Item.GateMsgID) and the HMAC label token. Those are
// mechanical functions of the fixture bytes, not fold behaviour.

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"golang.org/x/crypto/chacha20poly1305"

	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/state"
	rdsync "github.com/3dl-dev/ready/pkg/sync"
)

// Fixture secrets. Fixed (not random) so the vector file is reproducible and a
// reviewer can re-derive every pubkey by hand. These are TEST keys published in
// the repo on purpose — never use them for anything.
const (
	secOwner    = "1111111111111111111111111111111111111111111111111111111111111111"
	secMaint    = "2222222222222222222222222222222222222222222222222222222222222222"
	secAgent    = "3333333333333333333333333333333333333333333333333333333333333333"
	secOutsider = "4444444444444444444444444444444444444444444444444444444444444444"

	boardD = "ready"

	// t0 is the base event time (2023-11-14T22:13:20Z). Every case offsets from it.
	t0 = int64(1700000000)

	// Fixed confidential-board material. cekGood is the key the card is sealed
	// under; cekWrong is a different, valid 32-byte key a non-granted-but-keyed
	// reader might hold for the same slot (the "wrong CEK" case).
	cekGoodHex  = "a0a1a2a3a4a5a6a7a8a9aaabacadaeafb0b1b2b3b4b5b6b7b8b9babbbcbdbebf"
	cekWrongHex = "5f5e5d5c5b5a59585756555453525150" + "4f4e4d4c4b4a49484746454443424140"
	ltkHex      = "c0c1c2c3c4c5c6c7c8c9cacbcccdcecfd0d1d2d3d4d5d6d7d8d9dadbdcdddedf"
)

// builder carries the fixture identities and accumulates vectors.
type builder struct {
	owner, maint, agent, outsider *nostr.Key
	ownerPub, maintPub            string
	agentPub, outsiderPub         string
	boardCoord                    string
	vectors                       []Vector
}

// Build constructs the full vector file and verifies every hand-authored
// expectation against rd's live fold. A disagreement is returned as an error —
// the file is never written with an expectation the implementation contradicts,
// and an expectation is never "corrected" by copying the implementation without
// a human deciding which side is wrong.
func Build() (*File, error) {
	b := &builder{}
	var err error
	if b.owner, err = nostr.KeyFromHex(secOwner); err != nil {
		return nil, err
	}
	if b.maint, err = nostr.KeyFromHex(secMaint); err != nil {
		return nil, err
	}
	if b.agent, err = nostr.KeyFromHex(secAgent); err != nil {
		return nil, err
	}
	if b.outsider, err = nostr.KeyFromHex(secOutsider); err != nil {
		return nil, err
	}
	b.ownerPub = b.owner.PubKeyHex()
	b.maintPub = b.maint.PubKeyHex()
	b.agentPub = b.agent.PubKeyHex()
	b.outsiderPub = b.outsider.PubKeyHex()
	b.boardCoord = rdsync.BoardCoord(b.ownerPub, boardD)

	for _, fn := range []func() error{
		b.vCardFieldProjection,
		b.vLabelsFreeform,
		b.vCardLatestWins,
		b.vCardTieLowestID,
		b.vCardCarriedCreatedTagSurvivesRepublish,
		b.vDedupByEventID,
		b.vStatusChainHistory,
		b.vStatusSameSecondTiebreak,
		b.vStatusMissingStatusTag,
		b.vStatusNonAuthoritative,
		b.vBoardMaintainerAuthority,
		b.vBoardLatestWinsRevokesMaintainer,
		b.vBySpoofGuard,
		b.vStatusKind1633,
		b.vStatusWithoutCard,
		b.vDepChainBlocked,
		b.vDepTerminalEdges,
		b.vDepUnresolvableAndCrossBoard,
		b.vDepCycle,
		b.vGatePromotion,
		b.vGateUnderBlocking,
		b.vGateTerminalClears,
		b.vGateOpen,
		b.vGateApprove,
		b.vGateApproveUnderBlocking,
		b.vGateReject,
		b.vIssueDoesNotFold,
		b.vMalformedDropped,
		b.vForgedSignatureDropped,
		b.vUntrustedAuthorDropped,
		b.vTrustGateDisabledAdmitsAnyone,
		b.vBoardPinRejectsForeignBoard,
		b.vGrantRevocationPointInTime,
		b.vClaimSingleUseAcrossRevoke,
		b.vGrantCapOnlyOwnerGrantsMaintainer,
		b.vGrantCapContributorMayNotDelegate,
		b.vGrantCapOwnerIsIrrevocable,
		b.vGrantCapPeerMaintainerProtected,
		b.vRevokeBoundaryAtTheInstant,
		b.vGrantCoordinateBinding,
		b.vGrantReplayIsSharedByBothConsumers,
		b.vGrantLevelConfersStatusAuthority,
		b.vConfidentialGrantedReader,
		b.vConfidentialWrongCEK,
		b.vConfidentialNoDecryptor,
		b.vConfidentialNoTitlePlaceholder,
		b.vFoldGateQuarantine,
		b.vKeyringEpochZeroYieldsNoKey,
		b.vKeyringEpochZeroYieldsNoCutover,
		b.vKeyringRetainsEveryEpoch,
		b.vKeyringCutoverIgnoresGrantee,
		b.vConfidentialSinceEstablishesCutover,
		b.vConfidentialSinceNeverMovesTheCutoverLater,
		b.vConfidentialSinceForeignSignerIgnored,
		b.vConfidentialSinceAbsentIsTodaysBehaviour,
		b.vConfidentialSinceWithNoGrantsEstablishesTheInstantNotReadAccess,
		b.vViewsLattice,
		b.vItemTimestampEncoding,
		b.vItemTimestampAboveFloat64SafeBound,
		b.vItemContextContainsTimestampBytePattern,
		b.vCreatedTagRejectsNonCanonicalShapes,
	} {
		if err := fn(); err != nil {
			return nil, err
		}
	}

	f := &File{
		Version: FormatVersion,
		Spec:    "docs/design/board-fold-spec.md",
		Note: "rd board-fold conformance vectors. Each case replays `events` through the LIVE fold " +
			"(pkg/sync.ProjectItems, spec §1.1) with `options`, then applies the pkg/views predicates, " +
			"and MUST reproduce `expect` exactly: items field-for-field, views as SETS (never as ordered " +
			"lists — rendered order is not a total order, spec §15.7). Expectations are hand-authored from " +
			"the spec, not dumped from an implementation run. Regeneration is reproducible EXCEPT for the " +
			"confidential vectors, whose sealed Content carries a fresh random AEAD nonce per run (event " +
			"ids and schnorr signatures are deterministic), so those events differ byte-for-byte on every " +
			"regeneration even when no behaviour changed — review a regeneration, do not assume it is a no-op. " +
			"See `timestamp_encoding` for how expect.items[].created_at / updated_at are represented — they " +
			"are NOT bare JSON numbers. A vector whose `options.keyring` is non-null does NOT receive its " +
			"confidential-board key material as data: it names a reader SECRET plus a board coordinate, and " +
			"the client MUST derive the reader's CEKs and the board cutover from that vector's own " +
			"owner-signed kind-39301 grants (spec §11.10-§11.14) and wire the one derived keyring into BOTH " +
			"fold inputs (decryptor and encrypted-board set). Such a vector also carries `expect.keyring`, " +
			"which asserts the derived cutover and the current write epoch directly — the latter has no " +
			"item-level consequence on a read. `options.keyring` is never combined with " +
			"`options.decryptor` / `options.encrypted_boards`.",
		TimestampEncoding: "expect.items[].created_at and expect.items[].updated_at are unix-nanosecond " +
			"int64 values encoded as DECIMAL STRINGS (e.g. \"1700000000000000000\"), not bare JSON numbers. " +
			"Parse them with BigInt(field), never Number(field) or a generic JSON number parser: JavaScript's " +
			"Number cannot represent an arbitrary 64-bit integer exactly (Number.MAX_SAFE_INTEGER = " +
			"2^53-1 = 9007199254740991), and this field's declared type (arbitrary int64 unix nanoseconds) " +
			"is not restricted to values that happen to survive that. See board-fold-spec.md §4.8.",
		Keys: []Key{
			{Name: "owner", Secret: secOwner, Pubkey: b.ownerPub},
			{Name: "maintainer", Secret: secMaint, Pubkey: b.maintPub},
			{Name: "agent", Secret: secAgent, Pubkey: b.agentPub},
			{Name: "outsider", Secret: secOutsider, Pubkey: b.outsiderPub},
		},
		Vectors: b.vectors,
	}
	return f, nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func nanos(sec int64) int64 { return sec * int64(time.Second) }

func rfc(sec int64) string { return time.Unix(sec, 0).UTC().Format(time.RFC3339) }

// card builds and signs a 30302 card on the owner's board.
func (b *builder) card(k *nostr.Key, spec rdsync.CardSpec, createdAt int64) (*nostr.Event, error) {
	spec.BoardD = boardD
	spec.BoardAuthor = b.ownerPub
	return rdsync.BuildCardEvent(k, spec, createdAt)
}

// status builds and signs a NIP-34 status event carrying BOTH the card
// coordinate (first "a", what the fold reads) and the board coordinate (second
// "a", what the confidential fold gate reads) — the live write path's shape.
func (b *builder) status(k *nostr.Key, itemID, rdStatus, reason string, createdAt int64, env *rdsync.Envelope) (*nostr.Event, error) {
	return rdsync.BuildStatusEventWithIssueRoot(k, itemID, rdStatus, "", "", b.boardCoord, reason, createdAt, env)
}

// sealRaw re-implements pkg/sync's unexported sealContent (envelope.go, spec
// §3 wire format: base64Std(nonce(12) ‖ ChaCha20-Poly1305(CEK, nonce,
// plaintext))) so this package can construct a sealed Content blob carrying
// arbitrary — including MALFORMED — plaintext, not just a genuine marshaled
// cardPayload/statusPayload. pkg/sync deliberately does not export a seal
// primitive: production code only ever seals a real payload struct. This is
// fixture-construction-only, mirroring the documented wire format rather than
// reaching into pkg/sync's internals.
func sealRaw(cek [32]byte, plaintext []byte) (string, error) {
	aead, err := chacha20poly1305.New(cek[:])
	if err != nil {
		return "", fmt.Errorf("foldvectors: sealRaw: init aead: %w", err)
	}
	nonce := make([]byte, chacha20poly1305.NonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("foldvectors: sealRaw: read nonce: %w", err)
	}
	ct := aead.Seal(nil, nonce, plaintext, nil)
	out := make([]byte, 0, len(nonce)+len(ct))
	out = append(out, nonce...)
	out = append(out, ct...)
	return base64.StdEncoding.EncodeToString(out), nil
}

// cardWithRawContent builds and signs a normal 30302 card via spec (so tags —
// d, a, s, priority, itype, cek_epoch, enc — are the genuine write-path
// shape), then OVERWRITES Content with a hand-sealed blob and re-signs (the
// event id/signature cover Content, so a post-hoc Content swap must
// re-sign — the same pattern vFoldGateQuarantine uses for its non-base64
// malformed case). Used to construct a card that opens (AEAD succeeds) but
// does not carry the expected cardPayload shape.
func (b *builder) cardWithRawContent(k *nostr.Key, spec rdsync.CardSpec, createdAt int64, cek [32]byte, rawPlaintext []byte) (*nostr.Event, error) {
	ce, err := b.card(k, spec, createdAt)
	if err != nil {
		return nil, err
	}
	sealed, err := sealRaw(cek, rawPlaintext)
	if err != nil {
		return nil, err
	}
	ce.Content = sealed
	if err := ce.Sign(k); err != nil {
		return nil, fmt.Errorf("foldvectors: cardWithRawContent: re-sign: %w", err)
	}
	return ce, nil
}

// add records a vector after checking its hand-authored expectation against the
// live fold.
func (b *builder) add(v Vector) error {
	if len(v.SpecClauses) == 0 {
		return fmt.Errorf("vector %q cites no spec clause", v.Name)
	}
	gotItems, gotViews, gotLabels, err := Run(v)
	if err != nil {
		return fmt.Errorf("vector %q: run: %w", v.Name, err)
	}
	if err := sameItems(v.Expect.Items, gotItems); err != nil {
		return fmt.Errorf("vector %q: hand-authored items disagree with the live fold: %w", v.Name, err)
	}
	for name, want := range v.Expect.Views {
		got := gotViews[name]
		if !sameSet(want, got) {
			return fmt.Errorf("vector %q: view %q: expected %v, fold produced %v", v.Name, name, want, got)
		}
	}
	for name, got := range gotViews {
		if _, ok := v.Expect.Views[name]; !ok {
			return fmt.Errorf("vector %q: view %q not asserted (fold produced %v)", v.Name, name, got)
		}
	}
	for atom, want := range v.Expect.LabelViews {
		if !sameSet(want, gotLabels[atom]) {
			return fmt.Errorf("vector %q: label view %q: expected %v, fold produced %v", v.Name, atom, want, gotLabels[atom])
		}
	}
	// The derived-keyring expectation (spec §11.10-§11.14) gets the same
	// treatment as items and views: hand-authored from the clause, then CHECKED
	// against the live derivation. The two directions are both errors — an
	// asserted keyring the vector cannot derive, and a derived keyring the
	// vector forgot to assert — so a keyring-driven vector can never be added
	// with its epoch model unpinned.
	gotKeyring, err := KeyringFactsFor(v)
	if err != nil {
		return fmt.Errorf("vector %q: derive keyring: %w", v.Name, err)
	}
	switch {
	case v.Expect.Keyring == nil && gotKeyring != nil:
		return fmt.Errorf("vector %q: uses options.keyring but asserts no expect.keyring (derived %+v)", v.Name, *gotKeyring)
	case v.Expect.Keyring != nil && gotKeyring == nil:
		return fmt.Errorf("vector %q: asserts expect.keyring without options.keyring", v.Name)
	case v.Expect.Keyring != nil && *v.Expect.Keyring != *gotKeyring:
		return fmt.Errorf("vector %q: hand-authored keyring %+v disagrees with the live derivation %+v", v.Name, *v.Expect.Keyring, *gotKeyring)
	}
	b.vectors = append(b.vectors, v)
	return nil
}

// itemsJSON marshals hand-authored items, sorted by id, through EncodeItem so
// the hand-authored expectation and the live-fold comparison in Run() always
// agree on how created_at/updated_at are encoded (ready-414).
func itemsJSON(items ...*state.Item) ([]json.RawMessage, error) {
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	out := make([]json.RawMessage, 0, len(items))
	for _, it := range items {
		blob, err := EncodeItem(it)
		if err != nil {
			return nil, err
		}
		out = append(out, blob)
	}
	return out, nil
}

// sameItems compares two item-JSON lists field-for-field via a normalized decode
// (so key order never matters but an extra/missing field always fails).
func sameItems(want, got []json.RawMessage) error {
	if len(want) != len(got) {
		return fmt.Errorf("item count: expected %d, fold produced %d (%s)", len(want), len(got), strings.Join(idsIn(got), ","))
	}
	for i := range want {
		var w, g any
		if err := json.Unmarshal(want[i], &w); err != nil {
			return err
		}
		if err := json.Unmarshal(got[i], &g); err != nil {
			return err
		}
		if !reflect.DeepEqual(w, g) {
			return fmt.Errorf("item %d:\n  expected %s\n  produced %s", i, want[i], got[i])
		}
	}
	return nil
}

func idsIn(blobs []json.RawMessage) []string {
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

// sameSet compares two id lists as SETS (spec §15.7 forbids order assertions).
func sameSet(a, b []string) bool {
	as := append([]string(nil), a...)
	bs := append([]string(nil), b...)
	sort.Strings(as)
	sort.Strings(bs)
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

// vw is a terse constructor for the eight view sets. Any view not named is the
// empty set — but every view is still ASSERTED (add() rejects an unasserted
// view), so "empty" is a claim, not an omission.
func vw(pairs map[string][]string) map[string][]string {
	out := map[string][]string{
		"ready": {}, "work": {}, "pending": {}, "overdue": {},
		"delegated": {}, "my-work": {}, "gates": {}, "focus": {},
	}
	for k, v := range pairs {
		sort.Strings(v)
		out[k] = v
	}
	return out
}

func trust(pubkeys ...string) *[]string {
	s := append([]string(nil), pubkeys...)
	return &s
}

func hexKey(s string) ([32]byte, error) {
	var out [32]byte
	raw, err := hex.DecodeString(s)
	if err != nil {
		return out, err
	}
	if len(raw) != 32 {
		return out, fmt.Errorf("key must be 32 bytes, got %d", len(raw))
	}
	copy(out[:], raw)
	return out, nil
}

// labelTokenHex mirrors pkg/sync.labelToken (spec §10.3): hex(HMAC-SHA256(LTK,
// label)). Recomputed here rather than imported because it is unexported; it is
// a content hash, not fold behaviour.
func labelTokenHex(ltk [32]byte, label string) string {
	m := hmac.New(sha256.New, ltk[:])
	m.Write([]byte(label))
	return hex.EncodeToString(m.Sum(nil))
}
