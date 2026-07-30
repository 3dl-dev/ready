// Generator for web/board/src/lib/confidential.fixtures.ts (ready-c4b).
//
// Run from the repo root:
//
//	go run ./web/board/testdata/genconfidential/main.go > web/board/src/lib/confidential.fixtures.ts
//
// WHY A GENERATOR. The browser's confidential read path has to agree with the Go
// writer byte-for-byte across four independent layers: NIP-01 signing, the
// kind-39301 grant shape, the NIP-44 v2 wrap of the CEK, and the
// base64(nonce‖ChaCha20-Poly1305) content envelope. A hand-written TypeScript
// fixture would prove only that the TypeScript agrees with itself. Everything
// emitted below comes out of the REAL production Go code — pkg/sync's
// BuildBoardEvent / BuildRoleGrantEvent / BuildCardEvent, pkg/nip44.Seal,
// pkg/sync.sealContent via the Envelope — so a divergence in any one of those
// layers turns the browser suite red.
//
// This file lives under testdata/ so `go build ./...` and `go test ./...` never
// see it (the go tool skips testdata entirely); it is a tool, not a package.
//
// SECRET KEYS IN THE OUTPUT ARE TEST-ONLY. They are generated fresh on every run
// of this program, exist to let the browser suite stand in for a NIP-07 signer,
// and are never used for anything else. No production key material is involved.
package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"golang.org/x/crypto/chacha20poly1305"

	"github.com/3dl-dev/ready/pkg/nip44"
	"github.com/3dl-dev/ready/pkg/nostr"
	rdsync "github.com/3dl-dev/ready/pkg/sync"
)

const (
	boardD = "confboard"

	// Fixed created_at values keep the emitted event ids stable-ish per run and,
	// more importantly, order the grants and cards relative to the cutover.
	tBoard     = 1750000000
	tGrantE1   = 1750000100
	tCardE1A   = 1750000200
	tCardE1B   = 1750000210
	tRevoke    = 1750000300
	tGrantE2   = 1750000310
	tCardE2    = 1750000400
	tPreCut    = 1749999000 // genuinely before the cutover: grandfathered plaintext
	tPostCutPl = 1750000500 // after the cutover: smuggled cleartext, must quarantine

	// ready-daf ROUND 2 — the PARTIAL-omission material. Withholding the OLDEST
	// grants does not unset the cutover, it moves it LATER (BoardKeyring.cutover
	// is a min over the grants that were served), so the three constants below
	// exist to make that shift observable and detectable.
	//
	//	tGrantE1 .......... 1750000100  true cutover
	//	tGapPlaintext ..... 1750000205  plaintext, AFTER the true cutover
	//	tGrantE2 .......... 1750000310  cutover derived when epoch 1 is withheld
	//
	// tGapPlaintext sits strictly between them, so it is post-cutover cleartext
	// that MUST be quarantined, yet reads as pre-cutover — and gets grandfathered
	// in — the moment the epoch-1 grants are dropped. It is the payload of the
	// exploit, not a witness.
	tGapPlaintext = 1750000205
	// A LATE grant that still names epoch 1: a member added after the rotation
	// but given the epoch-1 key. Serving only this one leaves the epoch set
	// covered (min epoch is still 1) while pushing the derived cutover far past
	// the real one — the case only the created_at witness catches.
	tGrantE1Late = 1750000600
	// A card sealed under epoch 1 but published AFTER the rotation. Its
	// created_at is later than any grant, so it cannot witness a too-late cutover
	// by time; only its EPOCH does.
	tCardE1Late = 1750000700
	// ready-470: a legacy raw-payload grant and the hex re-wrap that replaces it in
	// the SAME addressable slot, one second later (the minimum step that makes a
	// relay serve the repair instead of the original, and small enough that the
	// board's derived cutover does not move).
	tGrantE3Legacy = 1750000800
	tGrantE3Rewrap = 1750000801
	// ready-470: a card SEALED UNDER EPOCH 3, so the difference between the two
	// grants above is visible where the item's done condition is written — a
	// rendered title on the board page — and not only in a derived key.
	tCardE3 = 1750000900
	// ready-02e: two cards whose ciphertext opens cleanly (real CEK, real AEAD
	// tag) but whose plaintext is NOT a {title: string, ...} card payload. The
	// AEAD succeeding must not be mistaken for a decryption SUCCESS. They are
	// conf-008 and conf-009; see the ITEM ID NOTE below.
	tCardNonObject = 1750000220
	tCardNoTitle   = 1750000230
)

// ITEM ID NOTE — ready-daf and ready-02e were written in parallel and BOTH
// originally minted conf-008/conf-009. That collision is not cosmetic: the fold
// keys a card by its item id and keeps the NEWEST revision, so ready-02e's
// sealed conf-008 (created_at 1750000220) silently displaced ready-daf's
// plaintext conf-008 (1750000205). An assertion of the form "the rendered set
// does not contain conf-008" then stops measuring whether the cleartext was
// withheld — it reports on whichever event won the fold, and would report a
// PASS for a withheld gap card and, worse, could MASK a leak by letting a
// later sealed revision of the same id stand in for a leaked plaintext one.
// ready-daf's two events were therefore renumbered to conf-010/conf-011.
// Every fixture card must have an id no other fixture card shares; the
// invariant is asserted in main.grantsomission.test.ts.

// sealRaw mirrors pkg/sync/envelope.go's unexported sealContent byte-for-byte
// (base64Std(nonce(12) || ChaCha20-Poly1305(cek, nonce, plaintext))) so this
// generator can seal a plaintext blob that is NOT a valid {title,...} card
// payload — sealCardPayload only ever marshals a well-formed cardPayload
// struct, so it cannot produce the malformed-shape fixtures ready-02e needs.
func sealRaw(cek [32]byte, plaintext []byte) (string, error) {
	aead, err := chacha20poly1305.New(cek[:])
	if err != nil {
		return "", err
	}
	nonce := make([]byte, chacha20poly1305.NonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ct := aead.Seal(nil, nonce, plaintext, nil)
	out := append(append([]byte{}, nonce...), ct...)
	return base64.StdEncoding.EncodeToString(out), nil
}

type expectedItem struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Context   string   `json:"context"`
	WaitingOn string   `json:"waiting_on"`
	Labels    []string `json:"labels"`
	Epoch     int      `json:"epoch"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "genconfidential:", err)
		os.Exit(1)
	}
}

func run() error {
	owner, err := nostr.GenerateKey()
	if err != nil {
		return err
	}
	member, err := nostr.GenerateKey()
	if err != nil {
		return err
	}
	revoked, err := nostr.GenerateKey()
	if err != nil {
		return err
	}
	stranger, err := nostr.GenerateKey()
	if err != nil {
		return err
	}
	// A member granted the epoch-1 key AFTER the rotation to epoch 2 (ready-daf
	// round 2). Its grant is emitted separately, not in `grants`, because it is
	// only ever served on its own.
	latecomer, err := nostr.GenerateKey()
	if err != nil {
		return err
	}

	cek1, err := rdsync.MintKey()
	if err != nil {
		return err
	}
	cek2, err := rdsync.MintKey()
	if err != nil {
		return err
	}
	ltk, err := rdsync.MintKey()
	if err != nil {
		return err
	}

	env1 := &rdsync.Envelope{CEK: cek1, Epoch: 1, LTK: &ltk}
	env2 := &rdsync.Envelope{CEK: cek2, Epoch: 2, LTK: &ltk}

	ownerPub := owner.PubKeyHex()
	coord := rdsync.BoardCoord(ownerPub, boardD)

	board, err := rdsync.BuildBoardEvent(owner, rdsync.BoardSpec{BoardD: boardD, Title: "Confidential Board"}, tBoard)
	if err != nil {
		return err
	}

	grant := func(grantee *nostr.Key, role string, cek [32]byte, epoch int, at int64, withLTK bool) (*nostr.Event, error) {
		spec := rdsync.RoleGrantSpec{
			BoardD:      boardD,
			BoardAuthor: ownerPub,
			Grantee:     grantee.PubKeyHex(),
			Role:        role,
		}
		if epoch > 0 {
			w, werr := rdsync.WrapKey(owner, grantee.PubKeyHex(), cek)
			if werr != nil {
				return nil, werr
			}
			spec.WrappedCEK, spec.CEKEpoch = w, epoch
			if withLTK {
				wl, lerr := rdsync.WrapKey(owner, grantee.PubKeyHex(), ltk)
				if lerr != nil {
					return nil, lerr
				}
				spec.WrappedLTK = wl
			}
		}
		return rdsync.BuildRoleGrantEvent(owner, spec, at)
	}

	var grants []*nostr.Event
	for _, g := range []struct {
		k       *nostr.Key
		role    string
		cek     [32]byte
		epoch   int
		at      int64
		withLTK bool
	}{
		{owner, rdsync.RoleOwner, cek1, 1, tGrantE1, true},
		{member, rdsync.RoleContributor, cek1, 1, tGrantE1, true},
		{revoked, rdsync.RoleContributor, cek1, 1, tGrantE1, true},
		// Rotation: epoch 2 is re-wrapped to the owner and the REMAINING member
		// only. The revoked key never receives it — that is forward secrecy.
		{owner, rdsync.RoleOwner, cek2, 2, tGrantE2, true},
		{member, rdsync.RoleContributor, cek2, 2, tGrantE2, true},
	} {
		e, gerr := grant(g.k, g.role, g.cek, g.epoch, g.at, g.withLTK)
		if gerr != nil {
			return gerr
		}
		grants = append(grants, e)
	}
	// The revocation grant itself: role=revoked, no CEK.
	rev, err := grant(revoked, rdsync.RoleRevoked, [32]byte{}, 0, tRevoke, false)
	if err != nil {
		return err
	}
	grants = append(grants, rev)

	// A CEK "grant" signed by a NON-OWNER (the stranger self-signs a grant to
	// itself carrying a key it minted). DeriveBoardKeyring must ignore it: only the
	// board author mints board keys.
	rogueCEK, err := rdsync.MintKey()
	if err != nil {
		return err
	}
	rogueWrap, err := rdsync.WrapKey(stranger, stranger.PubKeyHex(), rogueCEK)
	if err != nil {
		return err
	}
	rogue, err := rdsync.BuildRoleGrantEvent(stranger, rdsync.RoleGrantSpec{
		BoardD:      boardD,
		BoardAuthor: ownerPub,
		Grantee:     stranger.PubKeyHex(),
		Role:        rdsync.RoleContributor,
		WrappedCEK:  rogueWrap,
		CEKEpoch:    1,
	}, tGrantE1)
	if err != nil {
		return err
	}
	grants = append(grants, rogue)

	// ready-daf round 2: an owner CEK grant for epoch 1 signed LONG AFTER the
	// rotation. Emitted on its own (NOT in `grants`) so a test can serve exactly
	// this one grant and nothing else: the epoch set it covers still starts at 1,
	// so the epoch witness stays silent, while the cutover it establishes is
	// tGrantE1Late — half an hour after the truth.
	grantE1Late, err := grant(latecomer, rdsync.RoleContributor, cek1, 1, tGrantE1Late, false)
	if err != nil {
		return err
	}

	card := func(spec rdsync.CardSpec, env *rdsync.Envelope, at int64) (*nostr.Event, error) {
		spec.BoardD, spec.BoardAuthor, spec.Enc = boardD, ownerPub, env
		return rdsync.BuildCardEvent(owner, spec, at)
	}

	specE1A := rdsync.CardSpec{
		ItemID: "conf-001", Title: "Ship the confidential board UI",
		Context: "Titles must render for a granted member and fail closed otherwise.",
		Status:  "active", Priority: "p1", Type: "task",
		WaitingOn: "the relay allowlist", Labels: []string{"crypto", "board"},
	}
	specE1B := rdsync.CardSpec{
		ItemID: "conf-002", Title: "Rotate the CEK on revoke",
		Context: "Epoch N+1 is wrapped to the remaining members only.",
		Status:  "inbox", Priority: "p2", Type: "task",
	}
	specE2 := rdsync.CardSpec{
		ItemID: "conf-003", Title: "Sealed after the rotation",
		Context: "Only a reader holding epoch 2 can read this one.",
		Status:  "active", Priority: "p0", Type: "decision",
		Labels: []string{"crypto"},
	}

	cardE1A, err := card(specE1A, env1, tCardE1A)
	if err != nil {
		return err
	}
	cardE1B, err := card(specE1B, env1, tCardE1B)
	if err != nil {
		return err
	}
	cardE2, err := card(specE2, env2, tCardE2)
	if err != nil {
		return err
	}

	// ready-470: the pre-ready-c4b artifact and its repair, side by side.
	//
	// legacyRawWrapGrant seals an epoch-3 CEK the way WrapKey did BEFORE ready-c4b
	// — the raw 32 bytes. rd reads it (unwrapKey accepts both encodings); a browser
	// cannot, because NIP-07's nip44.decrypt returns a STRING and a UTF-8 decode
	// destroys those bytes with no error raised anywhere, which is why a board
	// carrying only such grants renders every card as [encrypted] even for its
	// owner. rewrappedHexGrant is what `rd confidential rewrap` publishes over it:
	// the SAME key value, the SAME epoch, the SAME addressable slot (so a relay
	// serves it instead), one second later, sealed as 64 hex characters.
	//
	// Both are emitted OUTSIDE `grants` so a test can serve exactly one of them and
	// compare what the reader ends up holding.
	cek3, err := rdsync.MintKey()
	if err != nil {
		return err
	}
	rawWrap3, err := nip44.Seal(owner, member.PubKeyHex(), cek3[:])
	if err != nil {
		return err
	}
	legacyRawWrapGrant, err := rdsync.BuildRoleGrantEvent(owner, rdsync.RoleGrantSpec{
		BoardD:      boardD,
		BoardAuthor: ownerPub,
		Grantee:     member.PubKeyHex(),
		Role:        rdsync.RoleContributor,
		WrappedCEK:  rawWrap3,
		CEKEpoch:    3,
	}, tGrantE3Legacy)
	if err != nil {
		return err
	}
	rewrappedHexGrant, err := grant(member, rdsync.RoleContributor, cek3, 3, tGrantE3Rewrap, false)
	if err != nil {
		return err
	}

	// ready-470: the SAME pair addressed to the OWNER's own key. ready-470's done
	// condition is written about the owner — "log into the board page with the
	// owner key and see real titles" — and the ready board's own epoch-1 owner
	// self-grant was one of the raw-payload ones. An owner is not privileged here:
	// its self-grant goes through the identical NIP-07 string boundary, which is
	// exactly why the board showed [encrypted] to the person who owns it.
	rawWrap3Owner, err := nip44.Seal(owner, ownerPub, cek3[:])
	if err != nil {
		return err
	}
	legacyRawWrapGrantOwner, err := rdsync.BuildRoleGrantEvent(owner, rdsync.RoleGrantSpec{
		BoardD:      boardD,
		BoardAuthor: ownerPub,
		Grantee:     ownerPub,
		Role:        rdsync.RoleOwner,
		WrappedCEK:  rawWrap3Owner,
		CEKEpoch:    3,
	}, tGrantE3Legacy)
	if err != nil {
		return err
	}
	rewrappedHexGrantOwner, err := grant(owner, rdsync.RoleOwner, cek3, 3, tGrantE3Rewrap, false)
	if err != nil {
		return err
	}

	// ready-470: the card those two grants are ABOUT. Sealed under epoch 3 by the
	// real Go writer, so whether its title reaches the screen is decided entirely
	// by whether the member's epoch-3 wrap survived the NIP-07 string boundary.
	// Served with legacyRawWrapGrant it must render the placeholder; served with
	// rewrappedHexGrant it must render this title. NOT part of `cards`, so the
	// existing board-wide fixtures are unchanged.
	epoch3Spec := rdsync.CardSpec{
		ItemID: "conf-012", Title: "Sealed under the re-wrapped epoch",
		Context: "Readable only by a member whose epoch-3 wrap survived the extension.",
		Status:  "active", Priority: "p1", Type: "task",
	}
	cardSealedUnderEpoch3, err := card(epoch3Spec, &rdsync.Envelope{CEK: cek3, Epoch: 3, LTK: &ltk}, tCardE3)
	if err != nil {
		return err
	}

	// A card sealed under an epoch NOBODY in this fixture holds (epoch 9). Even the
	// owner must render the placeholder for it — this is done-condition 3's
	// "an item at an epoch the reader lacks shows the placeholder while its
	// siblings still render".
	cek9, err := rdsync.MintKey()
	if err != nil {
		return err
	}
	cardE9, err := card(rdsync.CardSpec{
		ItemID: "conf-004", Title: "Sealed under an epoch nobody holds",
		Status: "active", Priority: "p3", Type: "task",
	}, &rdsync.Envelope{CEK: cek9, Epoch: 9, LTK: &ltk}, tCardE2)
	if err != nil {
		return err
	}

	// A post-cutover PLAINTEXT card: a rogue client smuggling cleartext onto a
	// confidential board. The clear title must never reach the DOM.
	cardSmuggled, err := card(rdsync.CardSpec{
		ItemID: "conf-005", Title: "SMUGGLED CLEARTEXT TITLE",
		Context: "SMUGGLED CLEARTEXT BODY", Status: "active", Priority: "p1", Type: "task",
	}, nil, tPostCutPl)
	if err != nil {
		return err
	}

	// A genuine PRE-cutover plaintext card: grandfathered, renders its clear title.
	cardGrandfathered, err := card(rdsync.CardSpec{
		ItemID: "conf-006", Title: "Legacy plaintext card",
		Context: "Authored before the board went confidential.", Status: "done", Priority: "p3", Type: "task",
	}, nil, tPreCut)
	if err != nil {
		return err
	}

	// A genuine confidential card whose SIGNATURE was then corrupted, exactly like
	// boardevents.fixtures.ts's forgedSig. A hostile relay serving it must not get
	// its (decryptable!) content rendered — signature verification runs first.
	forged, err := card(rdsync.CardSpec{
		ItemID: "conf-007", Title: "Forged card, valid ciphertext",
		Status: "active", Priority: "p0", Type: "task",
	}, env1, tCardE1A)
	if err != nil {
		return err
	}
	forged.Sig = "00" + forged.Sig[2:]

	// ready-daf round 2 — THE EXPLOIT PAYLOAD. Owner-signed PLAINTEXT at
	// tGapPlaintext, i.e. after the board went confidential (tGrantE1) but before
	// the rotation (tGrantE2). Against the true cutover it is post-cutover
	// cleartext and must be quarantined. Against the cutover a relay manufactures
	// by withholding the epoch-1 grants it looks pre-cutover, and the fold
	// grandfathers it — which is the fail-open this round closes. Nothing about
	// it is forged: the real Go writer signed it with the real owner key.
	gapSpec := rdsync.CardSpec{
		ItemID: "conf-010", Title: "GAP CLEARTEXT TITLE",
		Context: "GAP CLEARTEXT BODY", Status: "active", Priority: "p1", Type: "task",
	}
	cardGapPlaintext, err := card(gapSpec, nil, tGapPlaintext)
	if err != nil {
		return err
	}

	// A card sealed under epoch 1 and published AFTER the rotation (a writer with
	// a stale keyring). It is the pure EPOCH witness: its created_at is later than
	// every grant in the fixture, so it can never witness a too-late cutover by
	// time — only by naming an epoch no served grant covers.
	staleSpec := rdsync.CardSpec{
		ItemID: "conf-011", Title: "Sealed under epoch 1 after the rotation",
		Context: "A stale writer sealed this under the old epoch.",
		Status:  "active", Priority: "p2", Type: "task",
	}
	cardE1AfterRotation, err := card(staleSpec, env1, tCardE1Late)
	if err != nil {
		return err
	}
	// ready-02e: a genuinely confidential, correctly-signed card (member holds
	// epoch 1, the AEAD tag is real) whose sealed plaintext is a JSON ARRAY —
	// opens cleanly, is not an object at all. decryptCardPayload must treat
	// this as a decryption FAILURE, not a title-less success.
	cardNonObject, err := card(rdsync.CardSpec{
		ItemID: "conf-008", Title: "IGNORED — content is overwritten below",
		Status: "active", Priority: "p2", Type: "task",
	}, env1, tCardNonObject)
	if err != nil {
		return err
	}
	nonObjectSealed, err := sealRaw(cek1, []byte(`["not","an","object"]`))
	if err != nil {
		return err
	}
	cardNonObject.Content = nonObjectSealed
	if err := cardNonObject.Sign(owner); err != nil {
		return err
	}

	// ready-02e: same shape of defect, but the opened plaintext IS a JSON
	// object — just one with no string `title` field.
	cardNoTitle, err := card(rdsync.CardSpec{
		ItemID: "conf-009", Title: "IGNORED — content is overwritten below",
		Status: "active", Priority: "p2", Type: "task",
	}, env1, tCardNoTitle)
	if err != nil {
		return err
	}
	noTitleSealed, err := sealRaw(cek1, []byte(`{"context":"an object, but no title field at all"}`))
	if err != nil {
		return err
	}
	cardNoTitle.Content = noTitleSealed
	if err := cardNoTitle.Sign(owner); err != nil {
		return err
	}

	expected := []expectedItem{
		{ID: "conf-001", Title: specE1A.Title, Context: specE1A.Context, WaitingOn: specE1A.WaitingOn, Labels: specE1A.Labels, Epoch: 1},
		{ID: "conf-002", Title: specE1B.Title, Context: specE1B.Context, Labels: []string{}, Epoch: 1},
		{ID: "conf-003", Title: specE2.Title, Context: specE2.Context, Labels: specE2.Labels, Epoch: 2},
	}

	var b strings.Builder
	b.WriteString(header)
	b.WriteString("\nimport type { NostrEvent } from \"./nostrevent\";\n\n")

	fmt.Fprintf(&b, "export const BOARD_D = %q;\n", boardD)
	fmt.Fprintf(&b, "export const BOARD_COORD = %q;\n", coord)
	fmt.Fprintf(&b, "export const OWNER_PUB = %q;\n", ownerPub)
	fmt.Fprintf(&b, "export const OWNER_SEC = %q;\n", owner.SecretHex())
	fmt.Fprintf(&b, "export const MEMBER_PUB = %q;\n", member.PubKeyHex())
	fmt.Fprintf(&b, "export const MEMBER_SEC = %q;\n", member.SecretHex())
	fmt.Fprintf(&b, "export const REVOKED_PUB = %q;\n", revoked.PubKeyHex())
	fmt.Fprintf(&b, "export const REVOKED_SEC = %q;\n", revoked.SecretHex())
	fmt.Fprintf(&b, "export const STRANGER_PUB = %q;\n", stranger.PubKeyHex())
	fmt.Fprintf(&b, "export const STRANGER_SEC = %q;\n", stranger.SecretHex())
	fmt.Fprintf(&b, "export const LATECOMER_PUB = %q;\n", latecomer.PubKeyHex())
	fmt.Fprintf(&b, "export const LATECOMER_SEC = %q;\n\n", latecomer.SecretHex())

	fmt.Fprintf(&b, "/** The board-global cutover (created_at of the first CEK-bearing owner grant). */\nexport const CUTOVER = %d;\n\n", int64(tGrantE1))
	fmt.Fprintf(&b, "/** The cutover a relay MANUFACTURES by withholding the epoch-1 grants: the\n * rotation instant, %d seconds later than the truth (ready-daf round 2). */\nexport const CUTOVER_IF_EPOCH1_WITHHELD = %d;\n\n", int64(tGrantE2-tGrantE1), int64(tGrantE2))
	fmt.Fprintf(&b, "/** created_at of cardGapPlaintext — strictly between CUTOVER and\n * CUTOVER_IF_EPOCH1_WITHHELD. */\nexport const GAP_PLAINTEXT_AT = %d;\n\n", int64(tGapPlaintext))
	fmt.Fprintf(&b, "/** The clear strings cardGapPlaintext carries in its TAGS and CONTENT — what must\n * never reach the DOM. Exported so the assertion cannot drift from the writer. */\nexport const GAP_PLAINTEXT_TITLE = %q;\nexport const GAP_PLAINTEXT_BODY = %q;\n\n", gapSpec.Title, gapSpec.Context)
	fmt.Fprintf(&b, "/** The SEALED title of cardEpoch1AfterRotation — only a holder of the epoch-1 CEK\n * can produce it. */\nexport const EPOCH1_AFTER_ROTATION_TITLE = %q;\n\n", staleSpec.Title)
	fmt.Fprintf(&b, "/** Raw epoch keys, exported so envelope-level tests can seal/open without a keyring. */\nexport const CEK_EPOCH1 = %q;\nexport const CEK_EPOCH2 = %q;\nexport const LTK = %q;\n\n",
		hex.EncodeToString(cek1[:]), hex.EncodeToString(cek2[:]), hex.EncodeToString(ltk[:]))
	fmt.Fprintf(&b, "/** ready-470: the epoch-3 CEK carried by BOTH legacyRawWrapGrant and\n * rewrappedHexGrant — the value a re-wrap must leave untouched. */\nexport const CEK_EPOCH3 = %q;\n\n", hex.EncodeToString(cek3[:]))
	fmt.Fprintf(&b, "/** ready-470: the sealed title and context of cardSealedUnderEpoch3 — the text a\n * person sees on the board page once, and only once, the epoch-3 wrap is\n * browser-readable. */\nexport const EPOCH3_CARD_ID = %q;\nexport const EPOCH3_CARD_TITLE = %q;\nexport const EPOCH3_CARD_CONTEXT = %q;\n\n", epoch3Spec.ItemID, epoch3Spec.Title, epoch3Spec.Context)

	writeEvent(&b, "boardEvent", board)
	writeEvents(&b, "grants", grants)
	writeEvent(&b, "cardEpoch1A", cardE1A)
	writeEvent(&b, "cardEpoch1B", cardE1B)
	writeEvent(&b, "cardEpoch2", cardE2)
	writeEvent(&b, "cardEpochNobodyHolds", cardE9)
	writeEvent(&b, "cardSmuggledCleartext", cardSmuggled)
	writeEvent(&b, "cardGrandfatheredPlaintext", cardGrandfathered)
	writeEvent(&b, "cardForgedSignature", forged)
	writeEvent(&b, "cardSealedNonObject", cardNonObject)
	writeEvent(&b, "cardSealedNoTitle", cardNoTitle)

	b.WriteString("/** ready-daf round 2: the owner CEK grant for epoch 1 signed AFTER the rotation.\n * NOT part of `grants` — served alone, it establishes a cutover far later than the\n * truth while leaving the epoch set covered from 1. */\n")
	writeEvent(&b, "grantEpoch1Late", grantE1Late)
	b.WriteString("/** ready-daf round 2: owner-signed PLAINTEXT authored between the true cutover\n * and the rotation. Post-cutover cleartext that a manufactured late cutover\n * grandfathers in. NOT part of `cards`. */\n")
	writeEvent(&b, "cardGapPlaintext", cardGapPlaintext)
	b.WriteString("/** ready-daf round 2: sealed under epoch 1, published after the rotation. Its\n * epoch is the only thing about it that witnesses a withheld grant. NOT part of\n * `cards`. */\n")
	writeEvent(&b, "cardEpoch1AfterRotation", cardE1AfterRotation)

	b.WriteString("/** ready-470: an epoch-3 grant to MEMBER whose CEK is sealed as 32 RAW bytes,\n * the way WrapKey did before ready-c4b. rd reads it; a NIP-07 browser signer\n * returns a string and the bytes do not survive the UTF-8 decode, so the key is\n * silently destroyed. NOT part of `grants`. */\n")
	writeEvent(&b, "legacyRawWrapGrant", legacyRawWrapGrant)
	b.WriteString("/** ready-470: what `rd confidential rewrap` publishes over legacyRawWrapGrant —\n * the SAME CEK_EPOCH3 value, the same epoch, the same addressable `d` slot, one\n * second later, sealed as 64 hex characters. NOT part of `grants`. */\n")
	writeEvent(&b, "rewrappedHexGrant", rewrappedHexGrant)
	b.WriteString("/** ready-470: the same legacy/repaired pair addressed to the OWNER's own key —\n * the state the ready board's own self-grant was in, and the reason the board page\n * showed [encrypted] to the person who owns it. NOT part of `grants`. */\n")
	writeEvent(&b, "legacyRawWrapGrantOwner", legacyRawWrapGrantOwner)
	writeEvent(&b, "rewrappedHexGrantOwner", rewrappedHexGrantOwner)
	b.WriteString("/** ready-470: a card sealed under epoch 3, whose title renders in the page only\n * if the reader's epoch-3 wrap survived the NIP-07 string boundary. NOT part of\n * `cards`. */\n")
	writeEvent(&b, "cardSealedUnderEpoch3", cardSealedUnderEpoch3)

	b.WriteString("/** Every confidential card in the fixture, in relay-delivery order. */\n")
	b.WriteString("export const cards: NostrEvent[] = [\n  cardEpoch1A,\n  cardEpoch1B,\n  cardEpoch2,\n  cardEpochNobodyHolds,\n  cardSmuggledCleartext,\n  cardGrandfatheredPlaintext,\n  cardForgedSignature,\n  cardSealedNonObject,\n  cardSealedNoTitle,\n];\n\n")

	exp, err := json.MarshalIndent(expected, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintf(&b, "/** The plaintext the Go writer sealed, item for item — what the browser must reproduce. */\nexport const expectedPlaintext = %s as const;\n", exp)

	_, err = os.Stdout.WriteString(b.String())
	return err
}

func writeEvent(b *strings.Builder, name string, e *nostr.Event) {
	raw, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		panic(err)
	}
	fmt.Fprintf(b, "export const %s: NostrEvent = %s;\n\n", name, raw)
}

func writeEvents(b *strings.Builder, name string, es []*nostr.Event) {
	raw, err := json.MarshalIndent(es, "", "  ")
	if err != nil {
		panic(err)
	}
	fmt.Fprintf(b, "export const %s: NostrEvent[] = %s;\n\n", name, raw)
}

const header = `// GENERATED by web/board/testdata/genconfidential/main.go — DO NOT EDIT BY HAND.
//
//	go run ./web/board/testdata/genconfidential/main.go > web/board/src/lib/confidential.fixtures.ts
//
// A complete confidential-board scenario produced by the REAL Go writer
// (pkg/sync BuildBoardEvent / BuildRoleGrantEvent / BuildCardEvent, pkg/nip44.Seal,
// and the ChaCha20-Poly1305 content envelope). Because every byte here came out of
// production Go, the browser suite that consumes it is a cross-implementation
// conformance test, not a self-consistency check: if the TypeScript envelope, the
// grant parser, the NIP-44 unwrap or the NIP-01 id/signature derivation drifts from
// Go, these tests go red.
//
// SECRET KEYS BELOW ARE TEST-ONLY, freshly generated by the generator run that
// produced this file. They exist so the browser suite can stand in for a NIP-07
// signer (which is the only thing that ever holds a secret key in production — see
// nip07.ts). Nothing here is production key material, and nothing in
// index.html -> main.ts imports this module, so it is never bundled into dist/.
`
