package main

// `rd confidential rewrap` — ready-470.
//
// THE PROPERTY UNDER TEST IS NOT "the grants changed". A command that rotated to a
// new epoch, or that re-minted the CEK, or that re-sealed every card, would all
// leave a board whose grants carry hex wraps and would all be wrong: the first two
// orphan every existing card for anyone who does not also hold the old key, and the
// third rewrites signed history. So each test below asserts the thing the hex
// encoding is only half of — the key VALUE is byte-identical, the epoch is
// unchanged, the cards are untouched, the cutover stays where it was — alongside
// the encoding itself.
//
// The legacy artifact is built with pkg/nip44.Seal over the RAW 32 bytes, which is
// literally what WrapKey did before ready-c4b. Nothing here simulates the bug with
// a flag or a stub.
//
// EVERY RUN BELOW GOES THROUGH THE RELAY READ-BACK — no `--no-verify`. The board is
// stood up on newStoringRelay (confidential_selfheal_test.go), a real in-process
// NIP-01 relay in this package, seeded with the legacy grants; the command publishes
// over the websocket and then re-reads what it published with a fresh REQ. The
// assertions that matter are therefore made against the events THE RELAY HOLDS,
// reduced to the newest per addressable slot — which is exactly the set a browser
// receives — and not against the local log the command also wrote.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/spf13/pflag"

	"github.com/3dl-dev/ready/pkg/nip44"
	"github.com/3dl-dev/ready/pkg/nostr"
	rdSync "github.com/3dl-dev/ready/pkg/sync"
)

// legacyGrantsAt places the epoch-1 grants this far in the past, so a replacement
// stamped "now" is unmistakable next to one stamped original+1.
const legacyGrantAge = 100000

// legacyRawWrap seals key to grantee the way pkg/sync/keydist.go's WrapKey did
// BEFORE ready-c4b: the 32 raw bytes, not their hex. This is the payload a browser
// cannot recover, because NIP-07's nip44.decrypt returns a string and 32 random
// bytes do not survive a UTF-8 decode.
func legacyRawWrap(t *testing.T, owner *nostr.Key, granteePub string, key [32]byte) string {
	t.Helper()
	w, err := nip44.Seal(owner, granteePub, key[:])
	if err != nil {
		t.Fatalf("legacy raw wrap: %v", err)
	}
	return w
}

// legacyBoard is a confidential board bootstrapped ENTIRELY out of pre-ready-c4b
// grants: the owner self-grant plus one member grant, both carrying raw-byte wraps
// of the same epoch-1 CEK and LTK. It lives on a live in-process relay which holds
// those legacy grants, so a re-wrap has to REPLACE something in the slot a browser
// reads rather than write into an empty one.
type legacyBoard struct {
	dir     string
	owner   string
	boardD  string
	coord   string
	ownerK  *nostr.Key
	member  *nostr.Key
	cek     [32]byte
	ltk     [32]byte
	grantAt int64
	relay   *storingRelay
}

func setupLegacyRawWrapBoard(t *testing.T) legacyBoard {
	t.Helper()
	dir, owner := setupConfidentialProject(t)
	boardD := projectPrefix(dir)
	// setupConfidentialProject points the relay env at unreachableRelayURL; move
	// it to a relay that really speaks NIP-01 so `rd confidential rewrap` runs its
	// publish AND its read-back for real. Set after setup so it wins.
	relay := newStoringRelay(t)
	t.Cleanup(relay.close)
	t.Setenv("RD_NOSTR_RELAY_URL", relay.url())
	k, err := nostrKey()
	if err != nil {
		t.Fatalf("nostrKey: %v", err)
	}
	member, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	cek, err := rdSync.MintKey()
	if err != nil {
		t.Fatalf("MintKey: %v", err)
	}
	ltk, err := rdSync.MintKey()
	if err != nil {
		t.Fatalf("MintKey: %v", err)
	}
	at := time.Now().Unix() - legacyGrantAge

	lb := legacyBoard{
		dir: dir, owner: owner, boardD: boardD, coord: rdSync.BoardCoord(owner, boardD),
		ownerK: k, member: member, cek: cek, ltk: ltk, grantAt: at, relay: relay,
	}
	lb.appendLegacyGrant(t, owner, rdSync.RoleOwner, cek, ltk, 1, at)
	lb.appendLegacyGrant(t, member.PubKeyHex(), rdSync.RoleContributor, cek, ltk, 1, at)
	return lb
}

// appendLegacyGrant writes one owner-signed grant with raw-byte wraps into the
// local log AND onto the relay — the real state of a board granted before
// ready-c4b: rd reads it, and it is also the event the relay serves in that
// addressable slot, which is what makes the board page show [encrypted].
func (lb legacyBoard) appendLegacyGrant(t *testing.T, grantee, role string, cek, ltk [32]byte, epoch int, at int64) *nostr.Event {
	t.Helper()
	spec := rdSync.RoleGrantSpec{
		BoardD: lb.boardD, BoardAuthor: lb.owner, Grantee: grantee, Role: role,
		Label:      "legacy key (epoch 1)",
		WrappedCEK: legacyRawWrap(t, lb.ownerK, grantee, cek), CEKEpoch: epoch,
		WrappedLTK: legacyRawWrap(t, lb.ownerK, grantee, ltk),
	}
	ev, err := rdSync.BuildRoleGrantEvent(lb.ownerK, spec, at)
	if err != nil {
		t.Fatalf("BuildRoleGrantEvent: %v", err)
	}
	if _, err := rdSync.NewNostrLog(rdSync.NostrLogPath(lb.dir)).AppendUnique([]*nostr.Event{ev}); err != nil {
		t.Fatalf("append legacy grant: %v", err)
	}
	lb.relay.seed(ev)
	return ev
}

// runConfidentialArgv drives the REAL cobra path for `rd confidential ...`, so the
// flags, the plan and the output a human sees are what is under test — not a RunE
// reached around the parser. Flags are restored afterwards because cobra remembers
// what it parsed onto a package-level command.
//
// RESTORING A SLICE FLAG IS NOT Set(DefValue): pflag's stringArray APPENDS on Set,
// so `Set("verify", "[]")` leaves the array holding the two-character string "[]"
// and the next run in the same process tries to read its grants back from a relay
// literally named `[]`. Slice flags are emptied through SliceValue.Replace.
func runConfidentialArgv(t *testing.T, argv ...string) (string, error) {
	t.Helper()
	var sink strings.Builder
	rootCmd.SetErr(&sink)
	rootCmd.SetOut(&sink)
	defaults := map[string]string{}
	confidentialRewrapCmd.LocalNonPersistentFlags().VisitAll(func(f *pflag.Flag) { defaults[f.Name] = f.DefValue })
	t.Cleanup(func() {
		rootCmd.SetErr(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetArgs(nil)
		for name, def := range defaults {
			f := confidentialRewrapCmd.Flags().Lookup(name)
			if sv, ok := f.Value.(pflag.SliceValue); ok {
				_ = sv.Replace(nil)
				f.Changed = false
				continue
			}
			_ = confidentialRewrapCmd.Flags().Set(name, def)
		}
	})
	var runErr error
	out := captureStdoutPipe(t, func() {
		rootCmd.SetArgs(append([]string{"confidential"}, argv...))
		runErr = rootCmd.Execute()
	})
	return out, runErr
}

// logEvents / relayEvents are the two views a re-wrap can be judged against. The
// LOG is what the local machine wrote; the RELAY is what any other reader — the
// board page included — can actually obtain. Every done-condition assertion below
// uses the relay view, because a grant that exists only locally repairs nothing.
func logEvents(t *testing.T, lb legacyBoard) []*nostr.Event {
	t.Helper()
	events, err := rdSync.NewNostrLog(rdSync.NostrLogPath(lb.dir)).ReadAll()
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	return events
}

func relayEvents(t *testing.T, lb legacyBoard) []*nostr.Event {
	t.Helper()
	return lb.relay.stored()
}

// cekGrantsFor returns every owner-signed CEK-bearing grant in events for
// (grantee, epoch), newest first is not assumed — the caller asserts over all.
func cekGrantsFor(t *testing.T, lb legacyBoard, events []*nostr.Event, grantee string, epoch int) []*nostr.Event {
	t.Helper()
	var out []*nostr.Event
	for _, e := range events {
		if e.Kind != rdSync.KindRoleGrant || e.PubKey != lb.owner {
			continue
		}
		if tagVal1(e, "a") != lb.coord || tagVal1(e, "p") != grantee || tagVal1(e, "cek") == "" {
			continue
		}
		if tagVal1(e, "cek_epoch") != strconv.Itoa(epoch) {
			continue
		}
		out = append(out, e)
	}
	return out
}

// newestGrant returns the grant with the highest created_at for (grantee, epoch)
// out of events — the one a NIP-01 relay keeps in that addressable slot, i.e. the
// only one a browser ever sees.
func newestGrant(t *testing.T, lb legacyBoard, events []*nostr.Event, grantee string, epoch int) *nostr.Event {
	t.Helper()
	var best *nostr.Event
	for _, e := range cekGrantsFor(t, lb, events, grantee, epoch) {
		if best == nil || e.CreatedAt > best.CreatedAt || (e.CreatedAt == best.CreatedAt && e.ID > best.ID) {
			best = e
		}
	}
	if best == nil {
		t.Fatalf("no owner-signed epoch-%d CEK grant for %s", epoch, shortKey(grantee))
	}
	return best
}

// servedGrant is newestGrant over what THE RELAY holds: the grant the board page
// is handed for that slot.
func servedGrant(t *testing.T, lb legacyBoard, grantee string, epoch int) *nostr.Event {
	t.Helper()
	return newestGrant(t, lb, relayEvents(t, lb), grantee, epoch)
}

// relayServedView reduces everything the relay holds to the newest event per
// ADDRESSABLE slot (kind, pubkey, d) — the NIP-01 replacement rule. That is the
// event set any fresh reader obtains, so a keyring derived from it is the keyring a
// browser (or a second machine) ends up with. Non-addressable events are kept.
func relayServedView(t *testing.T, lb legacyBoard) []*nostr.Event {
	t.Helper()
	type slot struct {
		kind   int
		pubkey string
		d      string
	}
	newest := map[slot]*nostr.Event{}
	var out []*nostr.Event
	for _, e := range relayEvents(t, lb) {
		if e == nil {
			continue
		}
		if e.Kind < 30000 || e.Kind >= 40000 {
			out = append(out, e)
			continue
		}
		k := slot{e.Kind, e.PubKey, tagVal1(e, "d")}
		cur, seen := newest[k]
		if seen && !(e.CreatedAt > cur.CreatedAt || (e.CreatedAt == cur.CreatedAt && e.ID > cur.ID)) {
			continue
		}
		newest[k] = e
	}
	for _, e := range newest {
		out = append(out, e)
	}
	return out
}

// relayIDs is the id set the relay currently holds, for a before/after diff of
// what a run actually put on the wire.
func relayIDs(t *testing.T, lb legacyBoard) map[string]*nostr.Event {
	t.Helper()
	out := map[string]*nostr.Event{}
	for _, e := range relayEvents(t, lb) {
		out[e.ID] = e
	}
	return out
}

// TestConfidentialRewrapMakesLegacyWrapsBrowserReadable is the done condition of
// ready-470, asserted on the four things that must ALL hold at once: the wrap a
// relay now serves is hex (so a NIP-07 signer's string return survives), the key
// inside it is byte-identical to the one it replaced, the epoch did not move, and
// no card event changed.
func TestConfidentialRewrapMakesLegacyWrapsBrowserReadable(t *testing.T) {
	lb := setupLegacyRawWrapBoard(t)

	cardID, err := runCreateNostr(mustDir(t), nostrCreateSpec{
		title: "SECRET under the legacy epoch", context: "sealed with the raw-wrapped CEK",
		itemType: "task", priority: "p1",
	})
	if err != nil {
		t.Fatalf("create card: %v", err)
	}

	// PRECONDITION: what the board page receives today — the grant THE RELAY
	// serves in that slot — is unreadable to it. The owner's OWN wrap opens in Go
	// and carries the raw payload, which is exactly the state that renders
	// [encrypted] in the browser.
	before := servedGrant(t, lb, lb.owner, 1)
	gotCEK, browserSafe, err := rdSync.UnwrapKeyPayload(lb.ownerK, lb.owner, tagVal1(before, "cek"))
	if err != nil {
		t.Fatalf("owner cannot open its own legacy wrap: %v", err)
	}
	if browserSafe {
		t.Fatal("fixture is not the legacy artifact: its wrap already carries the hex payload")
	}
	if gotCEK != lb.cek {
		t.Fatal("fixture wrap does not carry the CEK it was built from")
	}
	beforeIDs := logSnapshot(t, lb.dir)
	beforeRelay := relayIDs(t, lb)

	// NO --no-verify: the command publishes over the websocket and then re-reads
	// each event back from the relay with a fresh REQ, schnorr-verifying it. A
	// relay that did not take them makes this call FAIL — see
	// TestConfidentialRewrapFailsWhenTheRelayDidNotTakeThem.
	out, err := runConfidentialArgv(t, "rewrap")
	if err != nil {
		t.Fatalf("rd confidential rewrap: %v\n%s", err, out)
	}
	if !strings.Contains(out, "2 grant(s) to re-wrap") {
		t.Fatalf("output does not name the two grants it re-wrapped:\n%s", out)
	}
	if !strings.Contains(out, "2/2 re-wrapped grant(s) present") {
		t.Fatalf("the read-back did not confirm both grants on the relay:\n%s", out)
	}

	// THE DONE CONDITION, asserted on what the RELAY now serves — the grant the
	// board page receives, not the copy the command kept locally.
	for _, grantee := range []string{lb.owner, lb.member.PubKeyHex()} {
		g := servedGrant(t, lb, grantee, 1)
		if g.CreatedAt <= before.CreatedAt {
			t.Fatalf("%s: the relay's newest grant for this slot is not newer than the legacy one (%d <= %d) — a NIP-01 relay would keep serving the raw wrap",
				shortKey(grantee), g.CreatedAt, before.CreatedAt)
		}
		if verr := g.Verify(); verr != nil {
			t.Fatalf("%s: the grant the relay serves does not verify: %v", shortKey(grantee), verr)
		}
		cek, safe, uerr := rdSync.UnwrapKeyPayload(lb.ownerK, grantee, tagVal1(g, "cek"))
		if uerr != nil {
			t.Fatalf("%s: re-wrapped CEK does not open: %v", shortKey(grantee), uerr)
		}
		if !safe {
			t.Fatalf("%s: the grant a relay now serves STILL carries a raw payload — the board page is no better off", shortKey(grantee))
		}
		if cek != lb.cek {
			t.Fatalf("%s: the re-wrap CHANGED the CEK value — every card sealed under it just became unreadable to this member", shortKey(grantee))
		}
		ltk, safeLTK, uerr := rdSync.UnwrapKeyPayload(lb.ownerK, grantee, tagVal1(g, "ltk"))
		if uerr != nil || !safeLTK || ltk != lb.ltk {
			t.Fatalf("%s: LTK re-wrap wrong (err=%v hex=%v same=%v)", shortKey(grantee), uerr, safeLTK, ltk == lb.ltk)
		}
	}

	// NO NEW EPOCH, derived the way a reader seeded ONLY from the relay derives it
	// — from the newest event per slot, which is all a browser is handed. A
	// rotation would also produce hex wraps and would orphan the card above for
	// anyone not holding epoch 1.
	kr := rdSync.DeriveBoardKeyring(relayServedView(t, lb), lb.ownerK, lb.owner, lb.boardD)
	if epochs := kr.Epochs(lb.coord); len(epochs) != 1 || epochs[0] != 1 {
		t.Fatalf("a relay-seeded owner now holds epochs %v, want exactly [1] — the re-wrap minted an epoch", epochs)
	}
	if got, ok := kr.CEK(lb.coord, 1); !ok || got != lb.cek {
		t.Fatal("the epoch-1 CEK a relay-seeded owner derives changed across the re-wrap")
	}

	// NOTHING BUT GRANTS WAS WRITTEN, and nothing already in the log was altered.
	after := logSnapshot(t, lb.dir)
	added := 0
	for id, e := range after {
		if _, existed := beforeIDs[id]; existed {
			continue
		}
		added++
		if e.Kind != rdSync.KindRoleGrant {
			t.Fatalf("re-wrap published a kind-%d event; it must only publish grants", e.Kind)
		}
	}
	if added != 2 {
		t.Fatalf("re-wrap added %d events, want exactly 2 (one per legacy grant)", added)
	}
	for id, e := range beforeIDs {
		if got := after[id]; got == nil || got.Content != e.Content || got.CreatedAt != e.CreatedAt {
			t.Fatalf("event %s was altered or dropped — existing history must be untouched", id[:12])
		}
	}

	// ...and the same holds ON THE WIRE: the relay received exactly two new events,
	// both grants. The local log cannot answer this — a command that also
	// re-published every card would look identical there if it skipped the log.
	sentToRelay := 0
	for id, e := range relayIDs(t, lb) {
		if _, existed := beforeRelay[id]; existed {
			continue
		}
		sentToRelay++
		if e.Kind != rdSync.KindRoleGrant {
			t.Fatalf("re-wrap sent a kind-%d event to the relay; it must only publish grants", e.Kind)
		}
	}
	if sentToRelay != 2 {
		t.Fatalf("the relay received %d new event(s), want exactly 2 (one per legacy grant)", sentToRelay)
	}

	// And the card still reads, through the real projection, for the owner.
	_, byID, err := nostrProjectAllItems()
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	if it := byID[cardID]; it == nil || it.Title != "SECRET under the legacy epoch" {
		t.Fatalf("the card stopped reading after the re-wrap: %+v", it)
	}
}

// TestConfidentialRewrapKeepsTheCutoverWhereItWas pins the created_at rule.
//
// A board's confidential CUTOVER is the EARLIEST created_at of any owner-signed
// CEK-bearing grant, and a relay serves only the newest event per slot — so a
// re-wrap stamped "now" moves the cutover a relay-seeded reader derives forward by
// the whole age of the board, and every plaintext card written in between starts
// reading as grandfathered instead of quarantined. The replacement is therefore
// stamped one second after the grant it replaces, and this test fails if anyone
// ever "simplifies" that to time.Now().
func TestConfidentialRewrapKeepsTheCutoverWhereItWas(t *testing.T) {
	lb := setupLegacyRawWrapBoard(t)
	if _, err := runConfidentialArgv(t, "rewrap"); err != nil {
		t.Fatalf("rewrap: %v", err)
	}

	g := servedGrant(t, lb, lb.owner, 1)
	if g.CreatedAt != lb.grantAt+1 {
		t.Fatalf("re-wrapped grant created_at = %d, want %d (the original + 1s)", g.CreatedAt, lb.grantAt+1)
	}

	// Derive the cutover the way a machine seeding ONLY from the relay does: what
	// the relay holds, reduced by NIP-01's newest-per-slot replacement rule, so the
	// legacy grants it also still holds are exactly as invisible as they are to a
	// browser.
	kr := rdSync.DeriveBoardKeyring(relayServedView(t, lb), lb.ownerK, lb.owner, lb.boardD)
	cutover, ok := kr.Cutover(lb.coord)
	if !ok {
		t.Fatal("a relay-only view of the re-wrapped grants no longer marks the board confidential")
	}
	if cutover != lb.grantAt+1 {
		t.Fatalf("relay-derived cutover = %d, want %d — the grandfather boundary moved by %d seconds",
			cutover, lb.grantAt+1, cutover-lb.grantAt)
	}
}

// TestConfidentialRewrapIsIdempotent: the second run must publish nothing. A
// command that republished every run would churn the relay and, because each run
// bumps created_at, would walk the cutover forward one second at a time.
func TestConfidentialRewrapIsIdempotent(t *testing.T) {
	lb := setupLegacyRawWrapBoard(t)
	if _, err := runConfidentialArgv(t, "rewrap"); err != nil {
		t.Fatalf("first rewrap: %v", err)
	}
	snapshot := logSnapshot(t, lb.dir)
	relaySnapshot := relayIDs(t, lb)

	out, err := runConfidentialArgv(t, "rewrap")
	if err != nil {
		t.Fatalf("second rewrap: %v", err)
	}
	if !strings.Contains(out, "nothing to re-wrap") {
		t.Fatalf("second run did not report itself a no-op:\n%s", out)
	}
	if got := logSnapshot(t, lb.dir); len(got) != len(snapshot) {
		t.Fatalf("second run published %d new event(s); a re-wrap of an already-hex board must publish nothing", len(got)-len(snapshot))
	}
	// Nor did it touch the wire: the first run's hex grants are what the relay
	// keeps serving, and a second run must not walk their created_at forward.
	if got := relayIDs(t, lb); len(got) != len(relaySnapshot) {
		t.Fatalf("second run sent %d new event(s) to the relay; an already-hex board must produce no traffic", len(got)-len(relaySnapshot))
	}
}

// TestConfidentialRewrapDryRunPublishesNothing — the preview has to be free.
func TestConfidentialRewrapDryRunPublishesNothing(t *testing.T) {
	lb := setupLegacyRawWrapBoard(t)
	snapshot := logSnapshot(t, lb.dir)
	relaySnapshot := relayIDs(t, lb)

	out, err := runConfidentialArgv(t, "rewrap", "--dry-run")
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if !strings.Contains(out, "2 grant(s) to re-wrap") || !strings.Contains(out, "nothing published") {
		t.Fatalf("dry run output is not a preview:\n%s", out)
	}
	if got := logSnapshot(t, lb.dir); len(got) != len(snapshot) {
		t.Fatalf("--dry-run published %d event(s)", len(got)-len(snapshot))
	}
	if got := relayIDs(t, lb); len(got) != len(relaySnapshot) {
		t.Fatalf("--dry-run sent %d event(s) to the relay", len(got)-len(relaySnapshot))
	}
}

// TestConfidentialRewrapWithholdsRevokedKeys. A revoked member still holds its old
// raw wrap and its rd CLI still reads the epoch it was given — that is the accepted
// limit of prospective revocation. What must NOT happen is the owner handing that
// key a fresh, browser-readable copy while re-wrapping everyone else.
func TestConfidentialRewrapWithholdsRevokedKeys(t *testing.T) {
	lb := setupLegacyRawWrapBoard(t)
	revoke, err := rdSync.BuildRoleGrantEvent(lb.ownerK, rdSync.RoleGrantSpec{
		BoardD: lb.boardD, BoardAuthor: lb.owner, Grantee: lb.member.PubKeyHex(), Role: rdSync.RoleRevoked,
	}, lb.grantAt+10)
	if err != nil {
		t.Fatalf("build revoke: %v", err)
	}
	if _, err := rdSync.NewNostrLog(rdSync.NostrLogPath(lb.dir)).AppendUnique([]*nostr.Event{revoke}); err != nil {
		t.Fatalf("append revoke: %v", err)
	}

	out, err := runConfidentialArgv(t, "rewrap")
	if err != nil {
		t.Fatalf("rewrap: %v", err)
	}
	if !strings.Contains(out, "REVOKED — withheld") {
		t.Fatalf("output does not report the revoked key as withheld:\n%s", out)
	}
	if !strings.Contains(out, "1 grant(s) to re-wrap") {
		t.Fatalf("expected exactly the owner self-grant to be re-wrapped:\n%s", out)
	}
	g := servedGrant(t, lb, lb.member.PubKeyHex(), 1)
	if _, safe, uerr := rdSync.UnwrapKeyPayload(lb.ownerK, lb.member.PubKeyHex(), tagVal1(g, "cek")); uerr != nil || safe {
		t.Fatalf("the revoked member's newest epoch-1 grant became browser-readable (hex=%v err=%v)", safe, uerr)
	}
}

// ackOnlyRelayURL is a NIP-01 relay that answers every EVENT with `OK true` and
// then serves NOTHING back. It is the failure this command was written to catch:
// rd's own publish report says "accepted" the moment any write relay says OK, so
// against this relay every local signal is green while the board page keeps
// receiving the raw-payload wrap. Without it, the passing read-back above would be
// evidence only that the assertion was reachable, not that it discriminates.
func ackOnlyRelayURL(t *testing.T) string {
	t.Helper()
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		conn, err := up.Upgrade(w, req, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			_, data, rerr := conn.ReadMessage()
			if rerr != nil {
				return
			}
			var frame []json.RawMessage
			if json.Unmarshal(data, &frame) != nil || len(frame) < 2 {
				continue
			}
			var typ string
			_ = json.Unmarshal(frame[0], &typ)
			switch typ {
			case "EVENT":
				var ev nostr.Event
				if json.Unmarshal(frame[1], &ev) == nil {
					_ = conn.WriteJSON([]any{"OK", ev.ID, true, ""})
				}
			case "REQ":
				var sub string
				_ = json.Unmarshal(frame[1], &sub)
				_ = conn.WriteJSON([]any{"EOSE", sub})
			}
		}
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

// TestConfidentialRewrapFailsWhenTheRelayDidNotTakeThem is the negative control for
// every read-back assertion in this file.
//
// The relay accepts the publish and serves nothing. The command must FAIL and name
// the relay: a re-wrap whose grants never reach the relay the board page reads has
// repaired nothing, and reporting success there is the exact way this item's bug
// would be declared fixed while ready.3dl.dev/board still showed [encrypted].
func TestConfidentialRewrapFailsWhenTheRelayDidNotTakeThem(t *testing.T) {
	setupLegacyRawWrapBoard(t)
	liar := ackOnlyRelayURL(t)
	t.Setenv("RD_NOSTR_RELAY_URL", liar)

	out, err := runConfidentialArgv(t, "rewrap")
	if err == nil {
		t.Fatalf("rewrap reported success against a relay that stored nothing:\n%s", out)
	}
	if !strings.Contains(err.Error(), liar) {
		t.Fatalf("the failure does not name the relay that did not take them: %v", err)
	}
	if !strings.Contains(err.Error(), "[encrypted]") {
		t.Fatalf("the failure does not tell the operator what it means for the board page: %v", err)
	}
	// It got as far as publishing — this is a read-back failure, not a refusal to
	// try, so the operator's next step really is "retry / relay repair".
	if !strings.Contains(out, "published 2 re-wrapped grant(s)") {
		t.Fatalf("expected the publish to have been attempted:\n%s", out)
	}
}

// TestConfidentialRewrapNoVerifyIsTheOnlyWayToSKIPTheReadBack keeps the escape
// hatch honest. `--no-verify` exists for an operator who cannot reach the read
// relays; it must succeed against the same lying relay the test above fails on, and
// it must SAY that the local log is now the only evidence — otherwise the flag is a
// silent way to declare this item fixed with nothing on the wire.
func TestConfidentialRewrapNoVerifyIsTheOnlyWayToSKIPTheReadBack(t *testing.T) {
	setupLegacyRawWrapBoard(t)
	t.Setenv("RD_NOSTR_RELAY_URL", ackOnlyRelayURL(t))

	out, err := runConfidentialArgv(t, "rewrap", "--no-verify")
	if err != nil {
		t.Fatalf("--no-verify must not fail on an unreadable relay: %v\n%s", err, out)
	}
	if !strings.Contains(out, "the local log is the only evidence") {
		t.Fatalf("--no-verify did not warn that nothing was confirmed on any relay:\n%s", out)
	}
}

// TestConfidentialRewrapRefusesANonOwner. Only the owner's signature is honoured
// as a source of board keys, so only the owner can re-mint one.
func TestConfidentialRewrapRefusesANonOwner(t *testing.T) {
	lb := setupLegacyRawWrapBoard(t)
	pub, ok, err := nostrPublisher()
	if err != nil || !ok {
		t.Fatalf("publisher: %v", err)
	}
	stranger, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	pub.Key = stranger
	if _, err := planBoardRewrap(lb.dir, pub, lb.owner, lb.boardD); err == nil {
		t.Fatal("a non-owner planned a re-wrap of someone else's board keys")
	} else if !strings.Contains(err.Error(), "only the board OWNER") {
		t.Fatalf("wrong refusal: %v", err)
	}
}

// TestConfidentialRewrapRefusesTwoKeysForOneEpoch. Re-wrapping recovers each key
// from the very wrap it replaces, so a grant carrying a DIFFERENT key for an epoch
// the owner already holds is a fork: re-wrapping it would silently declare one of
// the two the winner for that member. Refuse instead.
func TestConfidentialRewrapRefusesTwoKeysForOneEpoch(t *testing.T) {
	lb := setupLegacyRawWrapBoard(t)
	other, err := rdSync.MintKey()
	if err != nil {
		t.Fatalf("MintKey: %v", err)
	}
	rogue, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	lb.appendLegacyGrant(t, rogue.PubKeyHex(), rdSync.RoleContributor, other, lb.ltk, 1, lb.grantAt)

	pub, ok, err := nostrPublisher()
	if err != nil || !ok {
		t.Fatalf("publisher: %v", err)
	}
	if _, err := planBoardRewrap(lb.dir, pub, lb.owner, lb.boardD); err == nil {
		t.Fatal("planned a re-wrap over two different CEKs for one epoch")
	} else if !strings.Contains(err.Error(), "NOT the one the owner holds") {
		t.Fatalf("wrong refusal: %v", err)
	}
}
