package board

// The CONFIDENTIAL write round trip, both directions, in one test (ready-191).
//
// THE PROBLEM THIS CLOSES. `rd init` defaults to a CONFIDENTIAL board, and
// web/board's write path refused outright on such a board because the page had no
// seal path — so the browser write path worked on `--public` boards ONLY and
// every real project board was read-only in the browser. The refusal was right;
// what was missing was the seal that makes it unnecessary.
//
// WHY THIS IS NOT A VECTOR. testdata/write.vectors.json's own known_gaps records
// that confidential boards are not vector-covered on the GO side either: every
// vector forces Public:true because a sealed Content is nondeterministic (fresh
// nonce per seal) and so is not byte-comparable the way a plaintext tag array is.
// The claim worth asserting is not "the bytes equal a golden file" but "the OTHER
// implementation reads back exactly what was meant", and the only honest way to
// assert that is to run both implementations against each other. So:
//
//	rd seals a card          — pkg/sync.BuildCardEvent with a real Envelope
//	→ the browser opens it   — the SHIPPED lib/fold.ts projection, in node
//	→ the browser seals      — the SHIPPED board/writeevents.ts buildWrite
//	→ rd opens that          — pkg/sync.ProjectItems, below
//
// NO MOCKS ANYWHERE IN THE LOOP. The CEK and LTK are minted here with
// crypto/rand, the card the browser reads is produced by rd's real writer, the
// events the browser produces are signed here with a real secp256k1 key and
// folded by the real projection. The only substitution is the NIP-07 signer,
// which is a browser extension and cannot exist in a test — the browser side
// emits UNSIGNED events with the ids it computed, and this test signs them and
// asserts each signature reproduced exactly the id the browser derived. An
// extension that rewrote anything would break that equality, which is the same
// check nostrwriter.ts performs in production.
//
// It shells out to node via vite-node, the same pattern (and the same node/npm
// toolchain requirement) as timestamp_encoding_test.go and dist_test.go.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3dl-dev/ready/pkg/nostr"
	rdsync "github.com/3dl-dev/ready/pkg/sync"
)

// The free text the browser must seal. Every string is a sentinel with no
// plausible incidental occurrence, so "does this byte sequence appear in the
// published event" has exactly one possible answer and no substring of a
// coordinate, a tag name or a base64 body can mask a leak.
const (
	confTitle   = "SENTINEL-TITLE-e3a9-ship the confidential board"
	confContext = "SENTINEL-CONTEXT-71b4-the description nobody outside the board may read"
	confWaiting = "SENTINEL-WAITING-c05f-the relay allowlist"
	confLabelA  = "SENTINEL-LABEL-2d18"
	confLabelB  = "SENTINEL-LABEL-9fe6"
	confReason  = "SENTINEL-REASON-4ac2-moved from the browser"
	confBoardD  = "confwrite"
	confItemID  = "conf-w1"
)

// browserEvent is one unsigned event the browser produced, plus the id it
// derived for it.
type browserEvent struct {
	Kind      int        `json:"kind"`
	CreatedAt int64      `json:"created_at"`
	Tags      [][]string `json:"tags"`
	Content   string     `json:"content"`
	PubKey    string     `json:"pubkey"`
	ID        string     `json:"id"`
}

type browserOutput struct {
	Events     []browserEvent `json:"events"`
	ViewBefore *struct {
		Title    string `json:"title"`
		Context  string `json:"context"`
		Redacted bool   `json:"redacted"`
	} `json:"browser_view_before"`
}

// TestBrowserSealedWriteIsReadByRD is the head of the loop: everything below
// runs in one test because the steps are one causal chain and a partial run
// proves nothing.
func TestBrowserSealedWriteIsReadByRD(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Fatalf("node not found on PATH — required to run the browser's real write path against rd's "+
			"real reader: %v", err)
	}
	// dist_test.go's helper, reused deliberately rather than duplicated: it is
	// the package's single "make node_modules exist" step, and this test file
	// sorts BEFORE dist_test.go, so on a fresh checkout this is what runs the
	// one `npm ci` the package needs. Duplicating the logic would mean two
	// installs racing on a cold CI runner.
	ensureNodeModules(t)
	viteNode := filepath.Join("node_modules", ".bin", "vite-node")
	if _, err := os.Stat(viteNode); err != nil {
		t.Fatalf("%s is missing after npm ci (%v) — this test runs the SHIPPED TypeScript write path in a "+
			"real JS engine; there is no Go-side stand-in for it that would prove anything", viteNode, err)
	}

	key, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	owner := key.PubKeyHex()
	coord := rdsync.BoardCoord(owner, confBoardD)

	var cek, ltk [32]byte
	if _, err := rand.Read(cek[:]); err != nil {
		t.Fatalf("mint CEK: %v", err)
	}
	if _, err := rand.Read(ltk[:]); err != nil {
		t.Fatalf("mint LTK: %v", err)
	}
	env := &rdsync.Envelope{CEK: cek, Epoch: 1, LTK: &ltk}

	const tBoard, tCard, tWrite = 1750100000, 1750100100, 1750100200

	board, err := rdsync.BuildBoardEvent(key, rdsync.BoardSpec{
		BoardD: confBoardD, Title: "Confidential Write Board", Maintainers: []string{owner},
	}, tBoard)
	if err != nil {
		t.Fatalf("build board: %v", err)
	}

	// THE CARD RD ITSELF WOULD WRITE. Not a hand-built fixture: the real writer,
	// the real envelope, so what the browser reads in step 2 is genuinely rd's
	// output and a divergence in either implementation shows up here.
	rdCard, err := rdsync.BuildCardEvent(key, rdsync.CardSpec{
		ItemID: confItemID, Title: confTitle, Context: confContext, BoardD: confBoardD,
		BoardAuthor: owner, Status: "inbox", Priority: "p1", Type: "task",
		WaitingOn: confWaiting, Labels: []string{confLabelA, confLabelB},
		Enc: env,
	}, tCard)
	if err != nil {
		t.Fatalf("build rd's confidential card: %v", err)
	}
	// Sanity on the INPUT, so a failure downstream cannot be blamed on a card that
	// was never sealed in the first place.
	if strings.Contains(mustJSON(t, rdCard), confTitle) {
		t.Fatal("rd's own card leaked the title — the fixture is not confidential, abandoning the round trip")
	}

	out := runBrowserWrite(t, browserInput{
		Signer:      owner,
		BoardAuthor: owner,
		BoardD:      confBoardD,
		BoardCoord:  coord,
		CEKHex:      hex.EncodeToString(cek[:]),
		LTKHex:      hex.EncodeToString(ltk[:]),
		Epoch:       1,
		CreatedAt:   tWrite,
		Snapshot:    []*nostr.Event{board, rdCard},
		Op: map[string]any{
			"op": "update_status", "itemId": confItemID, "statusTo": "active", "note": confReason,
		},
	})

	// ── DIRECTION 1: rd sealed it, the browser opened it ──────────────────────
	if out.ViewBefore == nil {
		t.Fatal("the browser did not project rd's card at all")
	}
	if out.ViewBefore.Redacted {
		t.Fatalf("the browser could not decrypt rd's card (redacted) — it holds the CEK, so this is a "+
			"cross-implementation envelope divergence, not a missing key: %+v", *out.ViewBefore)
	}
	if out.ViewBefore.Title != confTitle || out.ViewBefore.Context != confContext {
		t.Fatalf("the browser opened rd's card to the WRONG text:\n got title=%q context=%q\nwant title=%q context=%q",
			out.ViewBefore.Title, out.ViewBefore.Context, confTitle, confContext)
	}

	// ── The events the browser wants to publish ───────────────────────────────
	if len(out.Events) != 2 {
		t.Fatalf("want exactly a card and a status event, got %d: %+v", len(out.Events), out.Events)
	}
	for _, e := range out.Events {
		if e.Kind == rdsync.KindIssue {
			t.Fatalf("a kind:%d NIP-34 issue event was published on a CONFIDENTIAL board. Its clear "+
				"`subject` tag is the item title and its content is the item context — the two fields the "+
				"envelope exists to seal. rd's Publisher.ensureIssueEvent suppresses it for exactly this "+
				"reason and the browser must too", rdsync.KindIssue)
		}
	}

	signed := make([]*nostr.Event, 0, len(out.Events))
	for i, be := range out.Events {
		if be.PubKey != owner {
			t.Fatalf("event %d: browser addressed it to pubkey %s, want %s", i, be.PubKey, owner)
		}
		e := &nostr.Event{Kind: be.Kind, CreatedAt: be.CreatedAt, Tags: be.Tags, Content: be.Content}
		if err := e.Sign(key); err != nil {
			t.Fatalf("event %d: sign: %v", i, err)
		}
		// The browser computes the id BEFORE signing (a nostr id is signature
		// independent) and the status event anchors to the card by that id. If Go's
		// canonical serialization and the browser's disagree by one byte, the anchor
		// points at an event that does not exist.
		if e.ID != be.ID {
			t.Fatalf("event %d: the browser derived id %s, rd's canonical serialization derived %s — the "+
				"status event's card anchor would point at nothing", i, be.ID, e.ID)
		}
		signed = append(signed, e)
	}

	// ── THE NO-PLAINTEXT CLAIM, asserted on the PUBLISHED EVENT ───────────────
	// Not on the projection: a projection that renders the right title proves the
	// reader works and says nothing about what went on the wire. This is the
	// serialized signed event, byte for byte what lib/publish.ts hands a relay.
	wire := mustJSON(t, signed)
	for _, secret := range []string{confTitle, confContext, confWaiting, confLabelA, confLabelB, confReason} {
		if strings.Contains(wire, secret) {
			t.Errorf("PLAINTEXT ON THE WIRE: %q appears in the published events of a confidential board.\n%s",
				secret, wire)
		}
	}
	// The clear routing tags MUST still be there — the envelope seals free text
	// only, and a card that sealed its routing would be unfilterable at the relay
	// and unreadable by the fold.
	card := signed[0]
	if got := tagVal(card, "s"); got != "active" {
		t.Errorf("card lost its clear status tag: got %q", got)
	}
	if got := tagVal(card, "a"); got != coord {
		t.Errorf("card lost its clear board coordinate: got %q want %q", got, coord)
	}
	if got := tagVal(card, "enc"); got != "1" {
		t.Errorf("card carries no enc marker: got %q", got)
	}
	if got := tagVal(card, "cek_epoch"); got != "1" {
		t.Errorf("card carries the wrong cek_epoch: got %q", got)
	}
	if got := tagVal(card, "title"); got != "" {
		t.Errorf("card carries a CLEAR title tag on a confidential board: %q", got)
	}
	if got := tagVal(card, "waiting_on"); got != "" {
		t.Errorf("card carries a CLEAR waiting_on tag on a confidential board: %q", got)
	}
	// The label tokens must be the ones rd's own writer produced for the same
	// labels under the same LTK — a relay's #l equality filter matches on exact
	// bytes, so a divergence here silently stops the browser's cards from matching
	// rd's for the same label.
	if got, want := tagVals(card, "l"), tagVals(rdCard, "l"); !equalStrings(got, want) {
		t.Errorf("label tokens diverge from rd's for the same labels and LTK:\n got %v\nwant %v", got, want)
	}

	// ── DIRECTION 2: the browser sealed it, rd opens it ───────────────────────
	log := append([]*nostr.Event{board, rdCard}, signed...)
	items := rdsync.ProjectItems(log, rdsync.ProjectOptions{
		PinnedBoard: coord,
		Decryptor:   fixedCEK{coord: coord, epoch: 1, cek: cek},
	})
	item, ok := items[confItemID]
	if !ok {
		t.Fatalf("rd's projection does not contain %s at all — a write rd cannot read back is exactly the "+
			"failure this test exists to catch", confItemID)
	}
	if item.Redacted {
		t.Fatalf("rd could not decrypt the browser's card (Redacted) — it holds the CEK the browser sealed "+
			"under, so this is an envelope divergence: title=%q", item.Title)
	}
	if item.Title != confTitle {
		t.Errorf("title: got %q want %q", item.Title, confTitle)
	}
	if item.Context != confContext {
		t.Errorf("context: got %q want %q", item.Context, confContext)
	}
	if item.WaitingOn != confWaiting {
		t.Errorf("waiting_on: got %q want %q", item.WaitingOn, confWaiting)
	}
	if !equalStrings(item.Labels, []string{confLabelA, confLabelB}) {
		t.Errorf("labels: got %v want %v", item.Labels, []string{confLabelA, confLabelB})
	}
	// THE AUTHORED status is "active" and it rides in the status event's CLEAR
	// `status` tag and in history. The PROJECTED status is "waiting", and that is
	// not a discrepancy: this item declares a waiting_on, and §9.8 derives
	// "waiting" for any non-terminal item that declares one — identically in
	// pkg/sync and in lib/fold.ts. Asserting "active" here would be asserting that
	// the fold's derivation is absent, which would be the real defect.
	if got := tagVal(signed[1], "status"); got != "active" {
		t.Errorf("the status event's clear status tag: got %q want %q", got, "active")
	}
	if item.Status != "waiting" {
		t.Errorf("projected status: got %q want %q (derived from the declared waiting_on, §9.8)",
			item.Status, "waiting")
	}
	if item.Priority != "p1" || item.Type != "task" {
		t.Errorf("clear routing fields lost: priority=%q type=%q", item.Priority, item.Type)
	}
	// The sealed status-event reason must come back too: on a status event the
	// reason is the ONLY free text, and it rides in history.
	if len(item.History) == 0 {
		t.Fatal("no history entry — the browser's status event did not fold")
	}
	last := item.History[len(item.History)-1]
	if last.Note != confReason {
		t.Errorf("history note: got %q want %q", last.Note, confReason)
	}
	if last.ToStatus != "active" {
		t.Errorf("history to_status: got %q want %q — the authored move is what history records",
			last.ToStatus, "active")
	}

	// ── ANTI-TAUTOLOGY: a reader WITHOUT the key gets the placeholder ──────────
	// Without this, every assertion above would still pass if the browser had
	// quietly published plaintext and rd had simply read the clear tags.
	blind := rdsync.ProjectItems(log, rdsync.ProjectOptions{PinnedBoard: coord})
	deaf, ok := blind[confItemID]
	if !ok {
		t.Fatal("keyless projection lost the item entirely")
	}
	if !deaf.Redacted {
		t.Errorf("a reader holding NO key was not fail-closed on the browser's card: title=%q", deaf.Title)
	}
	if strings.Contains(deaf.Title, "SENTINEL") || strings.Contains(deaf.Context, "SENTINEL") {
		t.Errorf("a reader holding NO key read the free text: title=%q context=%q", deaf.Title, deaf.Context)
	}
}

// ── plumbing ────────────────────────────────────────────────────────────────

type browserInput struct {
	Signer      string         `json:"signer"`
	BoardAuthor string         `json:"boardAuthor"`
	BoardD      string         `json:"boardD"`
	BoardCoord  string         `json:"boardCoord"`
	CEKHex      string         `json:"cekHex"`
	LTKHex      string         `json:"ltkHex"`
	Epoch       int            `json:"epoch"`
	CreatedAt   int64          `json:"createdAt"`
	Snapshot    []*nostr.Event `json:"snapshot"`
	Op          map[string]any `json:"op"`
}

// runBrowserWrite drives the SHIPPED TypeScript write path in a real JS engine
// and returns what it wants to publish.
func runBrowserWrite(t *testing.T, in browserInput) browserOutput {
	t.Helper()
	path := filepath.Join(t.TempDir(), "input.json")
	if err := os.WriteFile(path, []byte(mustJSON(t, in)), 0o600); err != nil {
		t.Fatalf("write browser input: %v", err)
	}
	cmd := exec.Command(filepath.Join("node_modules", ".bin", "vite-node"), "src/board/confwrite.emit.ts", path)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	stdout, err := cmd.Output()
	if err != nil {
		t.Fatalf("running the browser write path failed: %v\nstderr:\n%s", err, stderr.String())
	}
	var out browserOutput
	if err := json.Unmarshal(stdout, &out); err != nil {
		t.Fatalf("browser output is not JSON: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr.String())
	}
	return out
}

// fixedCEK is a BoardDecryptor holding exactly one (coord, epoch) key — the
// shape a granted member's keyring presents to the projection.
type fixedCEK struct {
	coord string
	epoch int
	cek   [32]byte
}

func (f fixedCEK) CEK(coord string, epoch int) ([32]byte, bool) {
	if coord == f.coord && epoch == f.epoch {
		return f.cek, true
	}
	return [32]byte{}, false
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

func tagVal(e *nostr.Event, name string) string {
	for _, tg := range e.Tags {
		if len(tg) >= 2 && tg[0] == name {
			return tg[1]
		}
	}
	return ""
}

func tagVals(e *nostr.Event, name string) []string {
	var out []string
	for _, tg := range e.Tags {
		if len(tg) >= 2 && tg[0] == name {
			out = append(out, tg[1])
		}
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
