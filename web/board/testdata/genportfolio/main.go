// Generator for web/board/src/lib/portfolio.fixtures.ts (ready-4d9).
//
// Run from the repo root:
//
//	go run ./web/board/testdata/genportfolio > web/board/src/lib/portfolio.fixtures.ts
//
// WHY A SECOND GENERATOR, next to genconfidential. confidential.fixtures.ts is
// ONE confidential board, which is all ready-c4b and ready-df0 needed. The
// whole-portfolio link is a statement about SEVERAL boards at once, and the
// properties that only exist across boards — a key never crossing from one board
// to another, one link opening some boards while others stay sealed, a notice
// that counts boards rather than naming one — cannot be witnessed on a
// single-board fixture at all.
//
// The scenario, all produced by the REAL Go writer (pkg/sync BuildBoardEvent /
// BuildRoleGrantEvent / BuildCardEvent, pkg/nip44.Seal, the ChaCha20-Poly1305
// envelope), so the browser suite consuming it is a cross-implementation
// conformance test rather than a self-consistency one:
//
//	alpha  — owned by the VIEWER, confidential, ROTATED: epochs 1 and 2, cards
//	         under both. The rotation is here so a portfolio link that shipped
//	         only current epochs would leave alpha's older card sealed.
//	beta   — owned by the VIEWER, confidential, epoch 1.
//	gamma  — owned by the VIEWER, confidential, and the viewer holds NO key: its
//	         CEK-bearing grant is addressed to a stranger. It is the fail-closed
//	         board — discovered, known to be confidential (the owner grant sets
//	         the cutover), and unreadable. Its cards must render the placeholder
//	         next to boards that opened.
//	delta  — owned by SOMEONE ELSE and granted to the viewer. It is why the page
//	         queries every owner named in the link's key material and not just
//	         the viewer: a viewer-only author filter would never discover it.
//
// AND THE CROSS-BOARD TRAP: gamma's cards are sealed under the SAME CEK BYTES as
// alpha's epoch 1. Nothing links the two boards — different coordinates,
// different grants — but it means a page that offered alpha's key to gamma would
// SUCCESSFULLY DECRYPT gamma and render its titles. That is what makes the
// per-board scope check testable at all: with distinct random keys per board, a
// leak would merely fail an AEAD and look identical to correct behaviour.
//
// This file lives under testdata/ so the go tool never builds it as a package.
//
// SECRET KEYS IN THE OUTPUT ARE TEST-ONLY, generated fresh on every run.
package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/3dl-dev/ready/pkg/nostr"
	rdsync "github.com/3dl-dev/ready/pkg/sync"
)

const (
	tBoard   = 1760000000
	tGrant1  = 1760000100
	tGrant2  = 1760000200
	tCard    = 1760000300
	tCardE2  = 1760000400
	tPreCut  = 1759999000
	tSmuggle = 1760000500
)

type expectedItem struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Coord string `json:"coord"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "genportfolio:", err)
		os.Exit(1)
	}
}

func run() error {
	viewer, err := nostr.GenerateKey()
	if err != nil {
		return err
	}
	otherOwner, err := nostr.GenerateKey()
	if err != nil {
		return err
	}
	stranger, err := nostr.GenerateKey()
	if err != nil {
		return err
	}

	mint := func() ([32]byte, error) { return rdsync.MintKey() }
	alpha1, err := mint()
	if err != nil {
		return err
	}
	alpha2, err := mint()
	if err != nil {
		return err
	}
	beta1, err := mint()
	if err != nil {
		return err
	}
	delta1, err := mint()
	if err != nil {
		return err
	}
	// gamma reuses alpha's epoch-1 key BYTES on purpose — see the header's
	// CROSS-BOARD TRAP note.
	gamma1 := alpha1

	viewerPub := viewer.PubKeyHex()
	otherPub := otherOwner.PubKeyHex()

	board := func(signer *nostr.Key, boardD, title string) (*nostr.Event, error) {
		return rdsync.BuildBoardEvent(signer, rdsync.BoardSpec{BoardD: boardD, Title: title, Maintainers: []string{signer.PubKeyHex()}}, tBoard)
	}
	grant := func(signer *nostr.Key, boardD, grantee string, cek [32]byte, epoch int, at int64) (*nostr.Event, error) {
		w, werr := rdsync.WrapKey(signer, grantee, cek)
		if werr != nil {
			return nil, werr
		}
		return rdsync.BuildRoleGrantEvent(signer, rdsync.RoleGrantSpec{
			BoardD: boardD, BoardAuthor: signer.PubKeyHex(), Grantee: grantee,
			Role: rdsync.RoleOwner, WrappedCEK: w, CEKEpoch: epoch,
		}, at)
	}
	card := func(signer *nostr.Key, boardD string, spec rdsync.CardSpec, env *rdsync.Envelope, at int64) (*nostr.Event, error) {
		spec.BoardD = boardD
		spec.Enc = env
		return rdsync.BuildCardEvent(signer, spec, at)
	}

	var events []*nostr.Event
	push := func(e *nostr.Event, err error) *nostr.Event {
		if err != nil {
			panic(err)
		}
		events = append(events, e)
		return e
	}

	// ---- alpha: viewer-owned, rotated ------------------------------------
	push(board(viewer, "alpha", "Alpha Board"))
	push(grant(viewer, "alpha", viewerPub, alpha1, 1, tGrant1))
	push(grant(viewer, "alpha", viewerPub, alpha2, 2, tGrant2))
	envA1 := &rdsync.Envelope{CEK: alpha1, Epoch: 1}
	envA2 := &rdsync.Envelope{CEK: alpha2, Epoch: 2}
	push(card(viewer, "alpha", rdsync.CardSpec{ItemID: "alpha-001", Title: "Alpha epoch one card", Context: "Sealed under alpha epoch 1.", Status: "active", Priority: "p1", Type: "task"}, envA1, tCard))
	push(card(viewer, "alpha", rdsync.CardSpec{ItemID: "alpha-002", Title: "Alpha after the rotation", Context: "Sealed under alpha epoch 2.", Status: "inbox", Priority: "p2", Type: "task"}, envA2, tCardE2))

	// ---- beta: viewer-owned, single epoch ---------------------------------
	push(board(viewer, "beta", "Beta Board"))
	push(grant(viewer, "beta", viewerPub, beta1, 1, tGrant1))
	envB1 := &rdsync.Envelope{CEK: beta1, Epoch: 1}
	push(card(viewer, "beta", rdsync.CardSpec{ItemID: "beta-001", Title: "Beta board card", Context: "Sealed under beta epoch 1.", Status: "active", Priority: "p0", Type: "decision"}, envB1, tCard))

	// ---- gamma: viewer-owned, viewer holds NO key -------------------------
	// The CEK grant is owner-signed (so the cutover is known to everyone) but
	// addressed to the stranger, so the viewer can never derive it.
	push(board(viewer, "gamma", "Gamma Board"))
	push(grant(viewer, "gamma", stranger.PubKeyHex(), gamma1, 1, tGrant1))
	envG1 := &rdsync.Envelope{CEK: gamma1, Epoch: 1}
	push(card(viewer, "gamma", rdsync.CardSpec{ItemID: "gamma-001", Title: "GAMMA SECRET TITLE", Context: "GAMMA SECRET BODY", Status: "active", Priority: "p1", Type: "task"}, envG1, tCard))
	// A genuine pre-cutover plaintext card on gamma: grandfathered, so the page
	// renders SOMETHING for gamma even though its sealed card stays sealed.
	push(card(viewer, "gamma", rdsync.CardSpec{ItemID: "gamma-002", Title: "Gamma legacy plaintext", Context: "Authored before gamma went confidential.", Status: "inbox", Priority: "p3", Type: "task"}, nil, tPreCut))
	// A post-cutover cleartext card: a rogue client smuggling plaintext onto a
	// confidential board. Must never reach the DOM, portfolio link or not.
	push(card(viewer, "gamma", rdsync.CardSpec{ItemID: "gamma-003", Title: "GAMMA SMUGGLED CLEARTEXT", Context: "GAMMA SMUGGLED BODY", Status: "active", Priority: "p1", Type: "task"}, nil, tSmuggle))

	// ---- delta: owned by someone else, granted to the viewer --------------
	push(board(otherOwner, "delta", "Delta Board"))
	push(grant(otherOwner, "delta", viewerPub, delta1, 1, tGrant1))
	envD1 := &rdsync.Envelope{CEK: delta1, Epoch: 1}
	push(card(otherOwner, "delta", rdsync.CardSpec{ItemID: "delta-001", Title: "Delta board card", Context: "A board owned by someone else.", Status: "active", Priority: "p1", Type: "task"}, envD1, tCard))

	coord := func(pub, d string) string { return rdsync.BoardCoord(pub, d) }

	expected := []expectedItem{
		{ID: "alpha-001", Title: "Alpha epoch one card", Coord: coord(viewerPub, "alpha")},
		{ID: "alpha-002", Title: "Alpha after the rotation", Coord: coord(viewerPub, "alpha")},
		{ID: "beta-001", Title: "Beta board card", Coord: coord(viewerPub, "beta")},
		{ID: "delta-001", Title: "Delta board card", Coord: coord(otherPub, "delta")},
	}

	var b strings.Builder
	b.WriteString(header)
	b.WriteString("\nimport type { NostrEvent } from \"./nostrevent\";\n\n")

	fmt.Fprintf(&b, "export const VIEWER_PUB = %q;\n", viewerPub)
	fmt.Fprintf(&b, "export const VIEWER_SEC = %q;\n", viewer.SecretHex())
	fmt.Fprintf(&b, "export const OTHER_OWNER_PUB = %q;\n", otherPub)
	fmt.Fprintf(&b, "export const STRANGER_PUB = %q;\n\n", stranger.PubKeyHex())

	fmt.Fprintf(&b, "export const ALPHA_COORD = %q;\n", coord(viewerPub, "alpha"))
	fmt.Fprintf(&b, "export const BETA_COORD = %q;\n", coord(viewerPub, "beta"))
	fmt.Fprintf(&b, "export const GAMMA_COORD = %q;\n", coord(viewerPub, "gamma"))
	fmt.Fprintf(&b, "export const DELTA_COORD = %q;\n\n", coord(otherPub, "delta"))

	b.WriteString("/** Raw CEKs. GAMMA_CEK is deliberately identical to ALPHA_CEK_EPOCH1 — see\n * the generator header's CROSS-BOARD TRAP note: it is what makes a key leaking\n * from one board to another VISIBLE instead of indistinguishable from a failed\n * AEAD. */\n")
	fmt.Fprintf(&b, "export const ALPHA_CEK_EPOCH1 = %q;\n", hex.EncodeToString(alpha1[:]))
	fmt.Fprintf(&b, "export const ALPHA_CEK_EPOCH2 = %q;\n", hex.EncodeToString(alpha2[:]))
	fmt.Fprintf(&b, "export const BETA_CEK = %q;\n", hex.EncodeToString(beta1[:]))
	fmt.Fprintf(&b, "export const GAMMA_CEK = %q;\n", hex.EncodeToString(gamma1[:]))
	fmt.Fprintf(&b, "export const DELTA_CEK = %q;\n\n", hex.EncodeToString(delta1[:]))

	// The EXACT keys= payload `rd board --portfolio --with-key` would print for
	// this viewer, produced by the production encoder
	// (pkg/sync.EncodePortfolioKeyBlob) over exactly the boards this viewer can
	// read. gamma is absent because the viewer holds no key for it.
	//
	// Emitting it here rather than re-encoding in TypeScript is the point: the
	// browser test drives a REAL Go-minted link, so main() -> parseFragment ->
	// decodePortfolioKeys is exercised against production bytes end to end.
	blob, err := rdsync.EncodePortfolioKeyBlob(map[string]map[int][32]byte{
		coord(viewerPub, "alpha"): {1: alpha1, 2: alpha2},
		coord(viewerPub, "beta"):  {1: beta1},
		coord(otherPub, "delta"):  {1: delta1},
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(&b, "/** The EXACT `keys=` payload `rd board --portfolio --with-key` prints for\n * this viewer, emitted by the production Go encoder. Covers alpha (both\n * epochs), beta and delta — NOT gamma, which this viewer cannot read. */\nexport const PORTFOLIO_KEYS_BLOB = %q;\n\n", blob)

	raw, err := json.MarshalIndent(events, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintf(&b, "/** Every event in the scenario, in relay-delivery order: 4 board definitions,\n * 5 role grants, 8 cards. */\nexport const snapshot: NostrEvent[] = %s;\n\n", raw)

	exp, err := json.MarshalIndent(expected, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintf(&b, "/** The plaintext the Go writer sealed on the boards the viewer CAN read. */\nexport const expectedPlaintext = %s as const;\n\n", exp)

	b.WriteString("/** Titles and bodies that must NEVER appear in the DOM for this viewer: gamma\n * is confidential and the viewer holds no key for it, and its post-cutover\n * cleartext card is quarantined by the fold gate. */\nexport const forbiddenText = [\n  \"GAMMA SECRET TITLE\",\n  \"GAMMA SECRET BODY\",\n  \"GAMMA SMUGGLED CLEARTEXT\",\n  \"GAMMA SMUGGLED BODY\",\n] as const;\n")

	_, err = os.Stdout.WriteString(b.String())
	return err
}

const header = `// GENERATED by web/board/testdata/genportfolio/main.go — DO NOT EDIT BY HAND.
//
//	go run ./web/board/testdata/genportfolio > web/board/src/lib/portfolio.fixtures.ts
//
// A FOUR-BOARD portfolio produced by the REAL Go writer (pkg/sync
// BuildBoardEvent / BuildRoleGrantEvent / BuildCardEvent, pkg/nip44.Seal, the
// ChaCha20-Poly1305 content envelope), for ready-4d9's whole-portfolio link.
// Read the generator's header for the scenario and — importantly — for why
// gamma's cards are sealed under alpha's epoch-1 key.
//
// SECRET KEYS BELOW ARE TEST-ONLY, freshly generated by the run that produced
// this file. Nothing in index.html -> main.ts imports this module, so it is
// never bundled into dist/.
`
