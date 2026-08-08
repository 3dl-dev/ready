package main

// ready-4d9 (follow-up): THE PARTIAL LINK THAT LIED ABOUT BEING COMPLETE.
//
// THE BUG THESE TESTS PIN. With a populated local log and an unreachable read
// relay, `rd board --portfolio --with-key` exited 0, printed a link carrying ONE
// board's keys out of the 47 the key demonstrably holds, and said on stderr:
//
//	WARNING: this link CARRIES THE READ KEYS FOR ALL 1 OF YOUR CONFIDENTIAL
//	BOARD ... anyone who opens it can read every title in your ENTIRE PORTFOLIO
//
// Both halves are wrong at once, and each hides the other: the link is narrower
// than asked for, and the sentence describing it is phrased so that it reads as
// true at any count. Nothing in the URL, the exit code or the warning showed the
// loss. It was hit on a FIRST live invocation, not by contrivance — the public
// relay is minReplicas=0 scale-to-zero, so a cold start is the owner's normal
// first-use path.
//
// THE INCOMPLETENESS CLASS, AND WHICH MEMBERS ARE REACHABLE FROM HERE. An
// unreachable relay is the LOUDEST member and was the first one fixed. The
// quietest is a relay that accepts the REQ, serves a SUBSET of what it holds, and
// sends EOSE: at every layer below the gate that is an ordinary success. This
// file therefore stands up BOTH — deadRelayURL and shortRelay — and the
// short-answer relay is the reason the file can claim anything about the class
// rather than about one member of it.
//
// WHAT IS ASSERTED HERE, and why each one is a separate test:
//
//	1. An UNREACHABLE relay refuses to mint (IncompleteGather...): no URL on
//	   stdout, a non-zero return, an error naming the relay and the way through.
//	2. A relay that ANSWERS SHORT of the local log is DETECTED (ShortAnswer...),
//	   named, and — since ready-1df — MINTED OVER by default while --strict still
//	   refuses with every word the old refusal carried. This is the member that
//	   walked through the first cut of the gate, because it is in Answered and not
//	   in Failed; it is also the member whose conclusion the owner's own portfolio
//	   forced a re-think of (32 boards that live only on his LAN relays made the
//	   conflated gate refuse on every invocation). See portfolioGather.lostRelay.
//	3. CROSS-RELAY DISAGREEMENT catches a short answer even with an EMPTY local
//	   log (ShortAnswerAcrossRelays...) — relay A serving 2 boards and relay B
//	   serving 1 is a proof about B that needs nothing from this machine.
//	4. --allow-partial mints over an UNREACHABLE relay, and the warning it prints
//	   never says "entire portfolio" (AllowPartial...): the count and the claim
//	   must agree. When a dead relay and a short one fire together the refusal
//	   leads with the dead one (UnreachableAndShortTogether...).
//	5. A COMPLETE gather still says "ENTIRE PORTFOLIO" (CompleteGather...): a gate
//	   that fired on every read would satisfy 1-3 and destroy the feature. Its
//	   relay is a FULL MIRROR of the local log, so "the relay answered" and "the
//	   gather was complete" are DIFFERENT events in the fixture and the test can
//	   tell which the gate keys on.
//	6. And that same complete link is SCOPED (CompleteGather...): it says what it
//	   could find, not that nothing was missed.
//	7. The LIMIT is witnessed, not just documented (ShortAnswerIsUndetectable...):
//	   where a relay withholds boards nothing else can prove exist, the command
//	   mints — and the wording is what keeps that honest.
//	8. A cold relay is RETRIED and the link is complete when the retry lands
//	   (ColdRelay...), and reported as a shortfall when it does not.
//	9. relayFetchMany can SEE a partial read where followFetch structurally
//	   cannot (RelayFetchMany...) — the fix is at the fetch, not only at the one
//	   caller that got bitten.
//	10. The gather applies the per-attempt deadline it documents
//	    (PerAttemptDeadline...), witnessed by what the relay actually receives.
//
// Every relay here is a REAL in-process NIP-01 relay (storingRelay, coldRelay or
// shortRelay) reached over a real websocket by the real nostr.FetchMany. Only the
// clock is shortened.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/rdconfig"
	rdSync "github.com/3dl-dev/ready/pkg/sync"
	"github.com/gorilla/websocket"
)

// deadRelayURL is a websocket URL nothing listens on. Port 9 (discard) is
// closed on the test host, so a dial fails FAST and deterministically — the same
// class of outcome as the reported repro's wss://127.0.0.1:9/nope.
const deadRelayURL = "ws://127.0.0.1:9/nope"

// coldRelay is a NIP-01 relay that IGNORES its first `stall` REQs — it accepts
// the websocket, then says nothing at all — and serves normally afterwards. That
// is the scale-to-zero cold start reproduced honestly: the TCP/WS handshake
// succeeds (the ingress is up), and the relay behind it is not awake yet, so the
// client hangs until its per-attempt deadline rather than getting a refusal.
type coldRelay struct {
	srv      *httptest.Server
	mu       sync.Mutex
	held     []*nostr.Event // what it serves once awake
	stall    int            // REQs still to be swallowed
	served   int            // REQs actually answered
	received int            // REQs seen at all, stalled or not
}

func newColdRelay(t *testing.T, stall int, events []*nostr.Event) *coldRelay {
	t.Helper()
	r := &coldRelay{stall: stall, held: events}
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		conn, err := up.Upgrade(w, req, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var frame []json.RawMessage
			if json.Unmarshal(data, &frame) != nil || len(frame) < 2 {
				continue
			}
			var typ string
			_ = json.Unmarshal(frame[0], &typ)
			if typ != "REQ" {
				continue
			}
			r.mu.Lock()
			r.received++
			cold := r.stall > 0
			if cold {
				r.stall--
			} else {
				r.served++
			}
			snap := append([]*nostr.Event(nil), r.held...)
			r.mu.Unlock()
			if cold {
				// Asleep: no EVENT, no EOSE, no error. The caller's per-attempt
				// deadline is the only thing that ends this.
				continue
			}
			var sub string
			_ = json.Unmarshal(frame[1], &sub)
			for _, e := range snap {
				_ = conn.WriteJSON([]any{"EVENT", sub, e})
			}
			_ = conn.WriteJSON([]any{"EOSE", sub})
		}
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *coldRelay) url() string { return "ws" + strings.TrimPrefix(r.srv.URL, "http") }
func (r *coldRelay) servedReqs() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.served
}

// receivedReqs is every REQ that reached the relay, including the ones it slept
// through. It is what witnesses how many ATTEMPTS the client actually made, which
// is the observable the per-attempt deadline governs.
func (r *coldRelay) receivedReqs() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.received
}

// shortRelay IS THE QUIET MEMBER OF THE INCOMPLETENESS CLASS: a real NIP-01
// relay that accepts the REQ, serves the events in `serve`, sends EOSE, and never
// mentions the events in `withheld` that it also holds.
//
// Nothing about that exchange is an error. The websocket opened, the subscription
// was accepted, events arrived, EOSE closed the stream. A client cannot tell it
// apart from a relay that genuinely holds only what it served — NIP-01 has no
// count, digest or continuation marker to check an answer against. That is why
// the gate cannot key on "the relay answered", and why this fixture had to exist
// before the file could claim anything about the class.
type shortRelay struct {
	srv      *httptest.Server
	mu       sync.Mutex
	serve    []*nostr.Event
	withheld []*nostr.Event
	reqCount int
}

func newShortRelay(t *testing.T, serve, withheld []*nostr.Event) *shortRelay {
	t.Helper()
	if len(withheld) == 0 {
		t.Fatal("a shortRelay that withholds nothing is a storingRelay — the fixture would not express a short answer")
	}
	r := &shortRelay{serve: serve, withheld: withheld}
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		conn, err := up.Upgrade(w, req, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var frame []json.RawMessage
			if json.Unmarshal(data, &frame) != nil || len(frame) < 2 {
				continue
			}
			var typ string
			_ = json.Unmarshal(frame[0], &typ)
			if typ != "REQ" {
				continue
			}
			var sub string
			_ = json.Unmarshal(frame[1], &sub)
			r.mu.Lock()
			r.reqCount++
			snap := append([]*nostr.Event(nil), r.serve...)
			r.mu.Unlock()
			for _, e := range snap {
				_ = conn.WriteJSON([]any{"EVENT", sub, e})
			}
			// A perfectly ordinary end-of-stored-events. The withheld events are
			// simply never mentioned.
			_ = conn.WriteJSON([]any{"EOSE", sub})
		}
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *shortRelay) url() string { return "ws" + strings.TrimPrefix(r.srv.URL, "http") }
func (r *shortRelay) reqs() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reqCount
}

// seed makes a storingRelay hold events without anyone publishing them through
// it, so a test can build a relay that is a FULL MIRROR of the local log.
func (r *storingRelay) seed(evs ...*nostr.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, evs...)
}

// portfolioLogEvents is everything portfolioEnv wrote to the project's signed
// local log. A relay seeded with it is a full mirror, which is what the
// anti-overfire control needs: with an EMPTY relay, "the relay answered" and "the
// relay served everything" are the same event and no test can tell which the gate
// keys on.
func portfolioLogEvents(t *testing.T, dir string) []*nostr.Event {
	t.Helper()
	evs, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
	if err != nil {
		t.Fatalf("read local log: %v", err)
	}
	if len(evs) == 0 {
		t.Fatal("the local log is empty — a mirror of it would prove nothing")
	}
	return evs
}

// grantsForBoard picks the kind-39301 grants for ONE board out of a log, by the
// board d-tag its grant coordinate names. It is how a test says "serve the pinned
// board's grants and withhold the sibling's" without depending on log order.
func grantsForBoard(evs []*nostr.Event, boardD string) []*nostr.Event {
	var out []*nostr.Event
	for _, e := range evs {
		if e == nil || e.Kind != rdSync.KindRoleGrant {
			continue
		}
		for _, tag := range e.Tags {
			if len(tag) >= 2 && tag[0] == "d" && strings.HasPrefix(tag[1], boardD+":") {
				out = append(out, e)
			}
		}
	}
	return out
}

// boardDFromCoord pulls the d-tag out of a "30301:<owner>:<d>" coordinate.
func boardDFromCoord(t *testing.T, coord string) string {
	t.Helper()
	parts := strings.SplitN(coord, ":", 3)
	if len(parts) != 3 {
		t.Fatalf("board coordinate %q is not 30301:<owner>:<d>", coord)
	}
	return parts[2]
}

// setProjectRelays declares MORE THAN ONE read relay for the project, which
// RD_NOSTR_RELAY_URL cannot express (it is a single URL). Cross-relay
// disagreement is only observable with two.
func setProjectRelays(t *testing.T, dir string, urls ...string) {
	t.Helper()
	sc, err := rdconfig.LoadSyncConfig(dir)
	if err != nil {
		t.Fatalf("LoadSyncConfig: %v", err)
	}
	sc.RelayEndpoints = nil
	for _, u := range urls {
		sc.RelayEndpoints = append(sc.RelayEndpoints, rdconfig.RelayEndpoint{URL: u, Read: true, Write: true})
	}
	if err := rdconfig.SaveSyncConfig(dir, sc); err != nil {
		t.Fatalf("SaveSyncConfig: %v", err)
	}
	if got := nostrReadRelays(); len(got) != len(urls) {
		t.Fatalf("read relays = %v, want the %d configured", got, len(urls))
	}
}

// offLogBoard mints a confidential board owned by `owner` and granted to `owner`,
// and returns its coordinate, CEK and the events that carry it — WITHOUT touching
// the local log. A board that exists only on a relay is the only way to test what
// happens when this machine cannot corroborate what a relay says.
func offLogBoard(t *testing.T, owner *nostr.Key, boardD string, at int64) (coord string, cek [32]byte, events []*nostr.Event) {
	t.Helper()
	self := owner.PubKeyHex()
	k, err := rdSync.MintKey()
	if err != nil {
		t.Fatalf("MintKey %s: %v", boardD, err)
	}
	wrapped, err := rdSync.WrapKey(owner, self, k)
	if err != nil {
		t.Fatalf("WrapKey %s: %v", boardD, err)
	}
	b, err := rdSync.BuildBoardEvent(owner, rdSync.BoardSpec{BoardD: boardD, Title: boardD, Maintainers: []string{self}}, at)
	if err != nil {
		t.Fatalf("BuildBoardEvent %s: %v", boardD, err)
	}
	g, err := rdSync.BuildRoleGrantEvent(owner, rdSync.RoleGrantSpec{
		BoardD: boardD, BoardAuthor: self, Grantee: self,
		Role: rdSync.RoleOwner, WrappedCEK: wrapped, CEKEpoch: 1,
	}, at+1)
	if err != nil {
		t.Fatalf("BuildRoleGrantEvent %s: %v", boardD, err)
	}
	return rdSync.BoardCoord(self, boardD), k, []*nostr.Event{b, g}
}

// shortRelayClock shrinks the per-attempt deadline so a stalled-relay test costs
// milliseconds instead of a minute. The retry COUNT is left at the product value:
// how many attempts a cold relay gets is behaviour under test, not scaffolding.
func shortRelayClock(t *testing.T, perAttempt time.Duration) {
	t.Helper()
	orig := portfolioRelayTimeout
	portfolioRelayTimeout = perAttempt
	t.Cleanup(func() { portfolioRelayTimeout = orig })
}

// TestBoardPortfolio_IncompleteGather_RefusesToMintALink IS THE DEFECT, pinned.
// The fixture is exactly the reported repro: real readable boards in the local
// log, one configured read relay that cannot be reached.
func TestBoardPortfolio_IncompleteGather_RefusesToMintALink(t *testing.T) {
	portfolioEnv(t)
	t.Setenv("RD_NOSTR_RELAY_URL", deadRelayURL)

	out, errOut, err := tryBoardPortfolioCmd(t, true, false)

	if err == nil {
		t.Fatalf("`rd board --portfolio --with-key` SUCCEEDED with an unreachable relay — it minted a link narrower than the user asked for and said nothing.\nstdout:\n%s\nstderr:\n%s", out, errOut)
	}
	// No link at all. A refusal that still printed the URL would leave the bad
	// link in the scrollback, which is where the bearer credential lives.
	if strings.Contains(out, "#") || strings.Contains(out, "keys=") {
		t.Errorf("the refusal still printed a link:\n%s", out)
	}
	msg := err.Error()
	// The user must be able to act on it: which relay, and what to do next.
	for _, want := range []string{deadRelayURL, "--allow-partial"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal does not mention %q — the user cannot act on it; error = %q", want, msg)
		}
	}
	if !strings.Contains(strings.ToUpper(msg), "INCOMPLETE") {
		t.Errorf("the refusal does not state that the gather was incomplete; error = %q", msg)
	}
	if !strings.Contains(msg, "never answered") {
		t.Errorf("the refusal does not distinguish an unreachable relay from one that answered short; error = %q", msg)
	}
	// And it must never carry the lie forward into the failure text.
	if strings.Contains(strings.ToLower(msg), "entire portfolio") {
		t.Errorf("the refusal claims 'entire portfolio' about a set it could not confirm; error = %q", msg)
	}
}

// TestBoardPortfolio_ShortAnswer IS THE MEMBER OF THE CLASS THAT WALKED THROUGH
// THE FIRST GATE, and — since ready-1df — the member whose CONCLUSION changed.
//
// The relay here is up, accepts the REQ, serves the pinned board's grants and
// sends EOSE. It never mentions the sibling board's grant, which it also holds.
// Under a gate keyed on "every relay answered", that relay is in Answered, is not
// in Failed, and the link is minted claiming the whole portfolio — the original
// ready-4d9 bug reproduced through a completely healthy-looking exchange. The
// DETECTION that catches it is unchanged and still exercised by both subtests
// below; a fixture that stopped detecting it fails them both.
//
// WHAT CHANGED IS WHAT THE DETECTION CONCLUDES BY DEFAULT (ready-1df). A relay
// that answered short costs this link nothing: every board it failed to serve is
// one this read already holds from the local log or another relay, so its key
// travels in the blob either way. What the shortfall proves is that the relay has
// not been GIVEN everything — a publishing gap, not a permission problem, and the
// board page already says exactly that. Refusing over it meant the owner's 32
// LAN-only boards made `rd board` refuse on every single invocation. So the
// default MINTS and names the gap, and --strict is the opt-in to the older,
// stricter reading — which is where every assertion about the refusal's WORDING
// now lives, unchanged.
//
// The detection is a FLOOR, not a proof: the local log independently holds a
// verified grant for the sibling board, so the relay demonstrably did not serve
// everything matching the filter that exists. See portfolioGather's doc for what
// this cannot reach, and TestBoardPortfolio_ShortAnswerIsUndetectableWithoutAFloor
// for that limit standing up as a fixture.
func TestBoardPortfolio_ShortAnswer(t *testing.T) {
	// shortAnswerEnv rebuilds the fixture per-subtest: portfolioEnv chdirs into a
	// fresh temp project and installs t.Cleanup, so it cannot be shared.
	shortAnswerEnv := func(t *testing.T) (pinnedCoord, siblingCoord string, pinned1, sibling [32]byte, relay *shortRelay) {
		t.Helper()
		_, pinnedCoord, siblingCoord, _, dir, pinned1, _, sibling, _ := portfolioEnv(t)
		log := portfolioLogEvents(t, dir)
		serve := grantsForBoard(log, boardDFromCoord(t, pinnedCoord))
		withheld := grantsForBoard(log, boardDFromCoord(t, siblingCoord))
		if len(serve) == 0 || len(withheld) == 0 {
			t.Fatalf("fixture is not a short answer: serving %d grant(s), withholding %d", len(serve), len(withheld))
		}
		relay = newShortRelay(t, serve, withheld)
		t.Setenv("RD_NOSTR_RELAY_URL", relay.url())
		return pinnedCoord, siblingCoord, pinned1, sibling, relay
	}

	// THE READY-1DF BEHAVIOUR: bare `rd board`, over exactly the fixture that used
	// to refuse, mints — and the link is not narrower for it.
	t.Run("default mints and names the publishing gap", func(t *testing.T) {
		pinnedCoord, siblingCoord, pinned1, sibling, relay := shortAnswerEnv(t)

		out, errOut, err := tryBoardCmd(t, boardFlags{})
		if err != nil {
			t.Fatalf("bare `rd board` refused over a relay that merely had not been given a board — that is the publishing gap ready-1df stopped refusing on: %v\nstderr:\n%s", err, errOut)
		}
		if relay.reqs() == 0 {
			t.Fatal("the short relay was never queried — this subtest would pass without exercising the gather at all")
		}

		// NOTHING WAS LOST. The withheld board's key is in the link, because the
		// local log had it all along. This is the load-bearing claim behind minting
		// rather than refusing, and it is asserted against the CEK the fixture
		// actually minted — not against anything read back out of the link.
		blob := parseKeysBlob(t, portfolioFragment(t, out).Get("keys"))
		if got, ok := blob[siblingCoord][1]; !ok || got != sibling {
			t.Fatalf("the link is missing the withheld board's real CEK (%s) — if minting cost a board, refusing was right after all", siblingCoord)
		}
		if got, ok := blob[pinnedCoord][1]; !ok || got != pinned1 {
			t.Fatal("the link carries no real key for the board the relay DID serve — the fixture never reached the gather")
		}

		notice := strings.TrimSpace(errOut)
		// It still says what the link IS.
		if !strings.Contains(notice, "WARNING") || !strings.Contains(strings.ToLower(notice), "bearer credential") {
			t.Errorf("the minted link stopped saying it is a bearer credential; warning = %q", notice)
		}
		// And it says what fell short, in the product's own terms.
		if !strings.Contains(strings.ToUpper(notice), "PUBLISHING GAP") {
			t.Errorf("the warning does not name the shortfall as a PUBLISHING GAP — that is the distinction ready-1df exists to draw; warning = %q", notice)
		}
		if !strings.Contains(notice, relay.url()) {
			t.Errorf("the warning does not name the relay that is behind; warning = %q", notice)
		}
		if !strings.Contains(notice, "SHORT") {
			t.Errorf("the warning no longer says the relay answered SHORT — the detection must still be reported, only its conclusion changed; warning = %q", notice)
		}
		// It must NOT be labelled PARTIAL: the link is not narrower, and spending
		// that word here would train the owner to ignore it on the one link where
		// a board really is missing.
		if strings.Contains(notice, "PARTIAL") {
			t.Errorf("a link that lost nothing was labelled PARTIAL; warning = %q", notice)
		}
	})

	// THE OLD READING, PRESERVED BEHIND --strict, with every wording assertion the
	// pre-ready-1df refusal carried.
	t.Run("--strict still refuses, and says which relay owes which board", func(t *testing.T) {
		pinnedCoord, siblingCoord, _, _, relay := shortAnswerEnv(t)

		out, errOut, err := tryBoardCmd(t, boardFlags{strict: true})
		if err == nil {
			t.Fatalf("--strict accepted a relay that served 1 of 2 boards and sent EOSE.\nstdout:\n%s\nstderr:\n%s", out, errOut)
		}
		if relay.reqs() == 0 {
			t.Fatal("the short relay was never queried — this subtest would pass without exercising the gather at all")
		}
		if strings.Contains(out, "#") || strings.Contains(out, "keys=") {
			t.Errorf("the refusal still printed a link:\n%s", out)
		}

		msg := err.Error()
		// The distinguishing fact: this relay ANSWERED. A message that called it
		// unreachable would send the operator to fix the wrong thing.
		if !strings.Contains(msg, "SHORT") {
			t.Errorf("the refusal does not say the relay answered SHORT; error = %q", msg)
		}
		if strings.Contains(msg, "never answered") {
			t.Errorf("a relay that answered was reported as unreachable; error = %q", msg)
		}
		if !strings.Contains(msg, relay.url()) {
			t.Errorf("the refusal does not name the relay that fell short; error = %q", msg)
		}
		// It names WHICH board is owed, not just that something is.
		if !strings.Contains(msg, siblingCoord) {
			t.Errorf("the refusal does not name the board the relay withheld (%s); error = %q", siblingCoord, msg)
		}
		if strings.Contains(msg, pinnedCoord) {
			t.Errorf("the refusal lists a board the relay DID serve (%s) as missing; error = %q", pinnedCoord, msg)
		}
		if strings.Contains(strings.ToLower(msg), "entire portfolio") {
			t.Errorf("the refusal claims 'entire portfolio'; error = %q", msg)
		}
		// And it names the way through, so --strict is not a dead end.
		if !strings.Contains(msg, "--strict") {
			t.Errorf("the refusal does not name the flag that caused it; error = %q", msg)
		}
	})
}

// TestBoardPortfolio_ShortAnswerAcrossRelays_DetectedWithNoLocalFloor is
// CROSS-RELAY DISAGREEMENT on its own.
//
// The local log holds NOTHING about these two boards, so the floor the previous
// test used does not exist here. The only evidence that relay B is short is that
// relay A served a verified grant for a board B never mentioned — a proof about B
// that needs nothing from this machine, and costs nothing extra because both
// relays were queried anyway.
//
// ready-1df: the DETECTION is what this test is about and it is unchanged. Its
// conclusion follows the same split as the single-relay case — --strict refuses
// and names the relay; the default mints, because relay A already supplied the
// board B was missing, so the link lost nothing. Both are asserted, and the
// anti-tautology control (two AGREEING relays must not trip anything) runs under
// --strict, where tripping is possible at all.
func TestBoardPortfolio_ShortAnswerAcrossRelays_DetectedWithNoLocalFloor(t *testing.T) {
	owner, _, _, dir := boardTestEnv(t)
	now := time.Now().Unix()
	coordA, cekA, eventsA := offLogBoard(t, owner, "relay-only-a", now)
	coordB, cekB, eventsB := offLogBoard(t, owner, "relay-only-b", now+10)
	both := append(append([]*nostr.Event{}, eventsA...), eventsB...)

	full := newStoringRelay(t)
	t.Cleanup(full.close)
	full.seed(both...)
	// Holds both, serves only A's — the same short answer, with no local log to
	// contradict it.
	short := newShortRelay(t, eventsA, eventsB)
	setProjectRelays(t, dir, full.url(), short.url())

	out, errOut, err := tryBoardCmd(t, boardFlags{strict: true})
	if err == nil {
		t.Fatalf("--strict accepted a read where relay A served 2 boards and relay B served 1.\nstdout:\n%s\nstderr:\n%s", out, errOut)
	}
	msg := err.Error()
	if !strings.Contains(msg, short.url()) {
		t.Errorf("the refusal does not name the SHORT relay; error = %q", msg)
	}
	if strings.Contains(msg, full.url()) {
		t.Errorf("the refusal blames the relay that served in full (%s); error = %q", full.url(), msg)
	}
	if !strings.Contains(msg, coordB) {
		t.Errorf("the refusal does not name the board only relay A served (%s); error = %q", coordB, msg)
	}
	if strings.Contains(msg, coordA) {
		t.Errorf("the refusal lists a board BOTH relays served (%s) as missing; error = %q", coordA, msg)
	}

	// AND THE DEFAULT MINTS OVER IT, carrying BOTH boards — including the one the
	// short relay never mentioned, which relay A supplied. That is the whole
	// argument for the ready-1df split, asserted on real CEK bytes: the
	// disagreement proves relay B is behind, and proves nothing about the link.
	outDefault, errDefault, errRun := tryBoardCmd(t, boardFlags{})
	if errRun != nil {
		t.Fatalf("the default refused over a cross-relay publishing gap: %v\nstderr:\n%s", errRun, errDefault)
	}
	gapBlob := parseKeysBlob(t, portfolioFragment(t, outDefault).Get("keys"))
	if got, ok := gapBlob[coordA][1]; !ok || got != cekA {
		t.Errorf("the minted link is missing board A's real CEK")
	}
	if got, ok := gapBlob[coordB][1]; !ok || got != cekB {
		t.Errorf("the minted link is missing the board the short relay withheld (%s) — it was served by relay A, so nothing was actually lost and minting must reflect that", coordB)
	}
	if !strings.Contains(strings.ToUpper(errDefault), "PUBLISHING GAP") {
		t.Errorf("the default's warning does not name the cross-relay shortfall as a publishing gap; stderr = %q", errDefault)
	}

	// ANTI-TAUTOLOGY. The disagreement is what refused under --strict, not the
	// mere presence of two relays: swap the short one for a SECOND full mirror —
	// same count, same fixture, same off-log boards — and --strict mints.
	second := newStoringRelay(t)
	t.Cleanup(second.close)
	second.seed(both...)
	setProjectRelays(t, dir, full.url(), second.url())
	out2, errOut2, err2 := tryBoardCmd(t, boardFlags{strict: true})
	if err2 != nil {
		t.Fatalf("two relays that AGREE must not trip the gate — the assertions above would then prove nothing: %v", err2)
	}
	blob := parseKeysBlob(t, portfolioFragment(t, out2).Get("keys"))
	if got, ok := blob[coordB][1]; !ok || got != cekB {
		t.Errorf("the agreed link is missing relay-only board B's real CEK — the relays were never actually read; stderr = %q", errOut2)
	}
	if strings.Contains(strings.ToUpper(errOut2), "PUBLISHING GAP") {
		t.Errorf("two relays that agree were reported as a publishing gap; stderr = %q", errOut2)
	}
}

// TestBoardPortfolio_ShortAnswerIsUndetectableWithoutAFloor WITNESSES THE LIMIT.
//
// The gate proves a FLOOR: a relay served less than the rest of the read proves
// exists. Where there IS no floor — one relay, an empty local log, and boards
// that exist nowhere else — a short answer is indistinguishable from a relay that
// holds only what it served. NIP-01 carries no count to check against.
//
// This test exists so that limit is a fixture rather than a sentence in a comment.
// The command MINTS here, and that is the correct behaviour: refusing would mean
// refusing every honest single-relay read. What makes it honest is the WORDING —
// the link says what the gather could find and states that a quiet subset is not
// detectable, instead of asserting the set is whole. A change that restored the
// absolute claim would leave this fixture minting a link that lies, so the
// assertions below are on the words.
func TestBoardPortfolio_ShortAnswerIsUndetectableWithoutAFloor(t *testing.T) {
	owner, _, _, dir := boardTestEnv(t)
	now := time.Now().Unix()
	shownCoord, shownCEK, shown := offLogBoard(t, owner, "shown-board", now)
	hiddenCoord, hiddenCEK, hidden := offLogBoard(t, owner, "hidden-board", now+10)

	relay := newShortRelay(t, shown, hidden)
	setProjectRelays(t, dir, relay.url())

	out, errOut, err := tryBoardPortfolioCmd(t, true, false)
	if err != nil {
		t.Fatalf("a single relay serving a subset nothing can contradict must still mint — refusing here refuses every honest single-relay read: %v", err)
	}

	// The loss is REAL, not hypothetical: the withheld board's key is genuinely
	// absent from the link, and nothing detected it.
	blob := parseKeysBlob(t, portfolioFragment(t, out).Get("keys"))
	if got, ok := blob[shownCoord][1]; !ok || got != shownCEK {
		t.Fatalf("the link does not carry the board the relay DID serve — the fixture never reached the gather")
	}
	if _, present := blob[hiddenCoord]; present {
		t.Fatalf("the withheld board is in the link — the relay served it after all, so this fixture is not a short answer")
	}
	assertNoKeyHex(t, out, hiddenCEK)

	// So the wording carries the whole weight. It must not assert the set is
	// exhaustive, and it must say the undetectable case exists.
	notice := strings.TrimSpace(errOut)
	if !strings.Contains(notice, "COULD FIND") {
		t.Errorf("the warning does not scope its count to what the gather could find — over this fixture that is a false completeness claim; warning = %q", notice)
	}
	if !strings.Contains(strings.ToLower(notice), "no shortfall was detectable") {
		t.Errorf("the warning does not say that a clean gather means no shortfall was DETECTABLE; warning = %q", notice)
	}
	if !strings.Contains(strings.ToUpper(notice), "SUBSET") {
		t.Errorf("the warning never mentions that a relay serving a subset cannot be caught — this fixture IS that case; warning = %q", notice)
	}
	if strings.Contains(strings.ToUpper(notice), "ALL 1 OF YOUR CONFIDENTIAL") {
		t.Errorf("the warning re-asserts the absolute claim this item removed; warning = %q", notice)
	}
}

// TestBoardPortfolio_AllowPartial_NeverClaimsTheEntirePortfolio is requirement 4
// standing alone: even on the opted-in path, the count and the claim must agree.
func TestBoardPortfolio_AllowPartial_NeverClaimsTheEntirePortfolio(t *testing.T) {
	_, _, siblingCoord, _, _, _, _, sibling, _ := portfolioEnv(t)
	t.Setenv("RD_NOSTR_RELAY_URL", deadRelayURL)

	out, errOut, err := tryBoardPortfolioCmd(t, true, true)
	if err != nil {
		t.Fatalf("--allow-partial must still mint a link: %v", err)
	}

	// POSITIVE CONTROL: the opt-in really produced the key-bearing link, so the
	// wording assertions below are about a real link and not about nothing. The
	// sibling board is one a per-directory command cannot reach, so its presence
	// also proves the local-log half of the gather still ran.
	v := portfolioFragment(t, out)
	if v.Get("keys") == "" {
		t.Fatalf("--allow-partial printed no keys= at all:\n%s", out)
	}
	blob := parseKeysBlob(t, v.Get("keys"))
	if got := blob[siblingCoord][1]; got != sibling {
		t.Errorf("the partial link does not carry the sibling board's real CEK; got %x want %x", got, sibling)
	}

	notice := strings.TrimSpace(errOut)
	lower := strings.ToLower(notice)
	if strings.Contains(lower, "entire portfolio") {
		t.Errorf("the PARTIAL link's warning claims 'entire portfolio' — the exact lie this item is about; warning = %q", notice)
	}
	if !strings.Contains(notice, "PARTIAL") {
		t.Errorf("the partial link's warning does not say it is PARTIAL; warning = %q", notice)
	}
	// It must name what was lost, or "partial" is a shrug.
	if !strings.Contains(notice, deadRelayURL) {
		t.Errorf("the partial warning does not name the relay that never answered; warning = %q", notice)
	}
	// It is still a bearer credential and must still say so.
	if !strings.Contains(notice, "WARNING") || !strings.Contains(lower, "bearer credential") {
		t.Errorf("the partial warning stopped saying the link is a bearer credential; warning = %q", notice)
	}
	// The count is still there, and it is the count of what the link ACTUALLY
	// carries (the fixture's 2 readable boards).
	if !strings.Contains(notice, " 2 ") {
		t.Errorf("the partial warning does not name how many boards' keys it carries; warning = %q", notice)
	}
}

// TestBoardPortfolio_UnreachableAndShortTogether_IsReportedAsLoss covers the case
// where BOTH members of the incompleteness class fire at once, which is where the
// ready-1df split could most easily be got wrong.
//
// A publishing gap alone mints. An unreachable relay alone refuses. Together, the
// refusal must win — there is a relay whose contents are genuinely unknown — and
// --allow-partial must then produce a link labelled PARTIAL that names the
// unreachable relay, not one that has been quietly downgraded to "just a
// publishing gap" because a short answer was also present.
func TestBoardPortfolio_UnreachableAndShortTogether_IsReportedAsLoss(t *testing.T) {
	_, pinnedCoord, siblingCoord, _, dir, pinned1, _, _, _ := portfolioEnv(t)
	log := portfolioLogEvents(t, dir)
	short := newShortRelay(t,
		grantsForBoard(log, boardDFromCoord(t, pinnedCoord)),
		grantsForBoard(log, boardDFromCoord(t, siblingCoord)))
	setProjectRelays(t, dir, short.url(), deadRelayURL)

	// DEFAULT: refuses, because one relay's contents are unknown.
	out, errOut, err := tryBoardCmd(t, boardFlags{})
	if err == nil {
		t.Fatalf("a dead relay alongside a short one minted a link anyway.\nstdout:\n%s\nstderr:\n%s", out, errOut)
	}
	msg := err.Error()
	if !strings.Contains(msg, deadRelayURL) || !strings.Contains(msg, "never answered") {
		t.Errorf("the refusal does not lead with the relay that never answered; error = %q", msg)
	}
	if strings.Contains(out, "#") {
		t.Errorf("the refusal still printed a link:\n%s", out)
	}

	// --allow-partial: mints, and the label is PARTIAL — the loss, not the gap.
	out2, errOut2, err2 := tryBoardCmd(t, boardFlags{allowPartial: true})
	if err2 != nil {
		t.Fatalf("--allow-partial must mint over an unreachable relay: %v", err2)
	}
	blob := parseKeysBlob(t, portfolioFragment(t, out2).Get("keys"))
	if got, ok := blob[pinnedCoord][1]; !ok || got != pinned1 {
		t.Fatal("the partial link carries no real key — the fixture never reached the gather")
	}
	notice := strings.TrimSpace(errOut2)
	if strings.Contains(strings.ToLower(notice), "entire portfolio") {
		t.Errorf("a link minted over an unreachable relay claims 'entire portfolio'; warning = %q", notice)
	}
	if !strings.Contains(notice, "PARTIAL") {
		t.Errorf("a link minted over an unreachable relay is not labelled PARTIAL; warning = %q", notice)
	}
	if !strings.Contains(notice, deadRelayURL) {
		t.Errorf("the warning does not name the relay that never answered; warning = %q", notice)
	}
	// The short relay is still reported too — the split changed conclusions, not
	// what gets observed.
	if !strings.Contains(notice, "SHORT") || !strings.Contains(notice, short.url()) {
		t.Errorf("the warning does not say WHICH relay answered short; warning = %q", notice)
	}
}

// TestBoardPortfolio_CompleteGather_StillClaimsTheEntirePortfolio is the
// anti-overfire control. A gate that refused (or downgraded the wording) on every
// read would satisfy every failure test above and quietly delete the feature.
//
// THE RELAY IS A FULL MIRROR OF THE LOCAL LOG, and that is the point of this
// version of the fixture. With an EMPTY relay, "the relay answered" and "the
// relay served everything it should have" are the SAME EVENT, so the test cannot
// say which fact the gate keys on — and a gate keyed on the weaker one passes.
// Here the two come apart: the short-answer tests above use a relay that also
// answers, and only this one serves in full.
func TestBoardPortfolio_CompleteGather_StillClaimsTheEntirePortfolio(t *testing.T) {
	_, _, _, _, dir, _, _, _, _ := portfolioEnv(t)
	relay := newStoringRelay(t)
	t.Cleanup(relay.close)
	relay.seed(portfolioLogEvents(t, dir)...)
	t.Setenv("RD_NOSTR_RELAY_URL", relay.url())

	out, errOut, err := tryBoardPortfolioCmd(t, true, false)
	if err != nil {
		t.Fatalf("a relay that answered AND served in full must not trip the gate: %v", err)
	}
	if relay.reqs() == 0 {
		t.Fatal("the relay was never queried — this test would pass without exercising the gather at all")
	}
	if v := portfolioFragment(t, out); v.Get("keys") == "" {
		t.Fatalf("the complete link carries no keys=:\n%s", out)
	}
	notice := strings.TrimSpace(errOut)
	if !strings.Contains(strings.ToLower(notice), "entire portfolio") {
		t.Errorf("a CONFIRMED-complete link no longer states its portfolio-wide blast radius; warning = %q", notice)
	}
	if strings.Contains(notice, "PARTIAL") {
		t.Errorf("a complete gather was labelled partial; warning = %q", notice)
	}
	if strings.Contains(notice, "SHORT") {
		t.Errorf("a relay that served in full was reported as short; warning = %q", notice)
	}

	// AND THE CLAIM IS SCOPED. "Every relay answered and none fell short" is a
	// FLOOR, and the warning may state only that. A version that says "ALL 2 OF
	// YOUR CONFIDENTIAL BOARDS" full stop is asserting a ceiling this command
	// cannot reach, which is the same class of lie as the original bug.
	if !strings.Contains(notice, "COULD FIND") {
		t.Errorf("the complete-path warning asserts an exhaustive set instead of what the gather could find; warning = %q", notice)
	}
	if !strings.Contains(strings.ToLower(notice), "no shortfall was detectable") {
		t.Errorf("the complete-path warning does not say what was actually established; warning = %q", notice)
	}
	if !strings.Contains(notice, "1 of 1 read relay") {
		t.Errorf("the scope clause does not say how many relays were consulted; warning = %q", notice)
	}
}

// TestBoardPortfolio_LocalOnly_SaysSoInsteadOfClaimingThePortfolio: with no read
// relays there is no relay whose contents could be missed, so the gate passes —
// but "nothing was asked of the network" is a materially different scope from
// "every relay answered in full", and the warning must not print the second when
// the first is what happened.
func TestBoardPortfolio_LocalOnly_SaysSoInsteadOfClaimingThePortfolio(t *testing.T) {
	portfolioEnv(t) // boardTestEnv clears RD_NOSTR_RELAY_URL; no relays configured

	if got := nostrReadRelays(); len(got) != 0 {
		t.Fatalf("fixture has read relays %v — this test is about having none", got)
	}
	out, errOut := runBoardPortfolioCmd(t, true)
	if portfolioFragment(t, out).Get("keys") == "" {
		t.Fatalf("a local-only project minted no keys:\n%s", out)
	}
	notice := strings.TrimSpace(errOut)
	if !strings.Contains(strings.ToLower(notice), "no read relays are configured") {
		t.Errorf("the local-only warning does not say the network was never asked; warning = %q", notice)
	}
	if strings.Contains(notice, "read relay(s) answered") {
		t.Errorf("a gather that asked nobody claims relays answered; warning = %q", notice)
	}
}

// TestBoardPortfolio_ColdRelayIsRetried covers the retry in both directions: a
// relay that wakes up on the retry yields a COMPLETE link, and a relay that never
// wakes is reported as a shortfall instead of being absorbed. The waking relay is
// a full mirror of the local log, so waking up is genuinely enough.
func TestBoardPortfolio_ColdRelayIsRetried(t *testing.T) {
	t.Run("wakes on the retry -> complete link", func(t *testing.T) {
		_, _, _, _, dir, _, _, _, _ := portfolioEnv(t)
		shortRelayClock(t, 300*time.Millisecond)
		relay := newColdRelay(t, 1, portfolioLogEvents(t, dir)) // asleep for exactly one REQ
		t.Setenv("RD_NOSTR_RELAY_URL", relay.url())

		out, errOut, err := tryBoardPortfolioCmd(t, true, false)
		if err != nil {
			t.Fatalf("a relay that woke on attempt 2 and served in full must produce a complete link, not a refusal: %v", err)
		}
		if relay.servedReqs() == 0 {
			t.Fatal("the relay never served a REQ — the retry did not happen, so this test proves nothing")
		}
		if !strings.Contains(strings.ToLower(errOut), "entire portfolio") {
			t.Errorf("the post-retry link is not reported as complete; stderr = %q", errOut)
		}
		if strings.Contains(errOut, "PARTIAL") {
			t.Errorf("a relay that answered on attempt 2 was still counted as missed; stderr = %q", errOut)
		}
		// The operator was told WHY the command paused, rather than appearing hung.
		if !strings.Contains(errOut, "retrying") {
			t.Errorf("nothing on stderr explained the wait before the retry; stderr = %q", errOut)
		}
		if v := portfolioFragment(t, out); v.Get("keys") == "" {
			t.Fatalf("no keys= in the post-retry link:\n%s", out)
		}
	})

	t.Run("never wakes -> reported, not absorbed", func(t *testing.T) {
		portfolioEnv(t)
		shortRelayClock(t, 200*time.Millisecond)
		relay := newColdRelay(t, 99, nil) // never wakes
		t.Setenv("RD_NOSTR_RELAY_URL", relay.url())

		out, _, err := tryBoardPortfolioCmd(t, true, false)
		if err == nil {
			t.Fatalf("a relay that never answered produced a link anyway:\n%s", out)
		}
		if !strings.Contains(err.Error(), relay.url()) {
			t.Errorf("the shortfall does not name the relay that timed out; error = %q", err)
		}
	})
}

// TestRelayFetchMany_SeesWhatFollowFetchCannot pins the fix AT THE FETCH. This is
// the project's recurring "any relay accepts" reduction (pkg/sync/relayclass.go
// reduceEventOutcome; ready-f7b; ready-260) and the reason this bug could not be
// noticed at the call site: with two relays, one live and one dead, followFetch
// returns a nil error, so there was no signal to check.
func TestRelayFetchMany_SeesWhatFollowFetchCannot(t *testing.T) {
	// The live relay must actually HOLD something, or the masking under test
	// never occurs: followFetch does surface an error when the merged result is
	// empty, and it is precisely the NON-empty case where the loss disappears.
	signer, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	held, err := rdSync.BuildBoardEvent(signer, rdSync.BoardSpec{BoardD: "held", Title: "held", Maintainers: []string{signer.PubKeyHex()}}, time.Now().Unix())
	if err != nil {
		t.Fatalf("BuildBoardEvent: %v", err)
	}
	live := newColdRelay(t, 0, []*nostr.Event{held})
	relays := []string{live.url(), deadRelayURL}
	filter := map[string]any{"kinds": []int{rdSync.KindBoard}}

	res := relayFetchMany(context.Background(), relays, filter, relayFetchOpts{PerAttempt: time.Second})
	if len(res.Events) != 1 {
		t.Fatalf("the live relay served %d events, want the 1 it holds — without it this test cannot exercise the masking", len(res.Events))
	}
	if res.complete() {
		t.Fatal("relayFetchMany reported a COMPLETE read with a dead relay in the set")
	}
	if len(res.Answered) != 1 || res.Answered[0] != live.url() {
		t.Errorf("Answered = %v, want exactly the live relay %q", res.Answered, live.url())
	}
	if len(res.Failed) != 1 || res.Failed[0].Relay != deadRelayURL {
		t.Errorf("Failed = %+v, want exactly the dead relay %q", res.Failed, deadRelayURL)
	}
	if !strings.Contains(res.shortfall(), deadRelayURL) {
		t.Errorf("shortfall() does not name the dead relay: %q", res.shortfall())
	}
	// PER-RELAY PROVENANCE, without which a short answer is unaskable: the merged
	// union cannot say WHICH relay supplied what.
	if len(res.PerRelay) != 1 || res.PerRelay[0].Relay != live.url() {
		t.Fatalf("PerRelay = %+v, want exactly the live relay's own events", res.PerRelay)
	}
	if len(res.PerRelay[0].Events) != 1 || res.PerRelay[0].Events[0].ID != held.ID {
		t.Errorf("PerRelay[0].Events = %+v, want the one event that relay served", res.PerRelay[0].Events)
	}

	// A live relay holding NOTHING is a success, not a failure: "the relay is up
	// and has no matching events" is the fact the old boolean could not express.
	empty := newColdRelay(t, 0, nil)
	only := relayFetchMany(context.Background(), []string{empty.url()}, filter, relayFetchOpts{PerAttempt: time.Second})
	if !only.complete() {
		t.Errorf("an empty-but-reachable relay was counted as a failure: %+v", only.Failed)
	}
	if len(only.Answered) != 1 {
		t.Errorf("an empty-but-reachable relay was not counted as having answered: %+v", only)
	}

	// THE MASKING ITSELF, asserted rather than assumed. Same relays, same filter:
	// followFetch reports no error at all, because one relay answered. That is
	// why the portfolio gather may not use it, and why the fix had to add a
	// return value rather than check one.
	evs, ferr := followFetch(context.Background(), relays, filter)
	if ferr != nil {
		t.Fatalf("followFetch's contract changed: it must stay forgiving for rd follow's best-effort reads; err = %v", ferr)
	}
	if len(evs) != 1 {
		t.Fatalf("followFetch returned %d events, want 1 — the masking case is a PARTIAL success, not an empty one", len(evs))
	}

	// Asking nobody is complete: a local-only project misses no relay.
	if none := relayFetchMany(context.Background(), nil, filter, relayFetchOpts{}); !none.complete() {
		t.Errorf("a read with zero relays was reported incomplete: %+v", none.Failed)
	}
}

// TestPortfolioGather_PerAttemptDeadlineIsTheOneTheFetchApplies replaces an
// arithmetic tautology.
//
// The test that used to live here computed `want := relays * attempts * timeout`
// and compared it to portfolioGatherBudget(n) — which is that same expression's
// one-line body. It held for ANY implementation of the expression, and it passed
// while the exact defect it was written against was reintroduced: the fetch
// applying nostr.DefaultTimeout instead of portfolioRelayTimeout. A budget is
// only worth anything if the FETCH APPLIES IT, so that is what is asserted here,
// against real relays, by what those relays actually receive.
//
// The two var invariants at the end are genuine and are kept.
func TestPortfolioGather_PerAttemptDeadlineIsTheOneTheFetchApplies(t *testing.T) {
	filter := map[string]any{"kinds": []int{rdSync.KindRoleGrant}}

	t.Run("relayFetchMany waits the deadline it was GIVEN, on every attempt", func(t *testing.T) {
		// No overall ctx deadline at all, so the per-attempt deadline is the ONLY
		// thing that can end an attempt — nothing else can be mistaken for it.
		relay := newColdRelay(t, 99, nil) // never wakes: every attempt must time out
		const perAttempt = 200 * time.Millisecond
		const attempts = 3

		start := time.Now()
		res := relayFetchMany(context.Background(), []string{relay.url()}, filter,
			relayFetchOpts{PerAttempt: perAttempt, Attempts: attempts})
		elapsed := time.Since(start)

		if res.complete() {
			t.Fatal("a relay that never answered was reported as having answered")
		}
		if got := relay.receivedReqs(); got != attempts {
			t.Errorf("the relay received %d REQ(s), want %d — the fetch did not make every attempt it was configured for", got, attempts)
		}
		// THE DEFECT, caught: applying nostr.DefaultTimeout (10s) per attempt
		// makes this take 30s instead of 600ms.
		if elapsed > 3*time.Second {
			t.Errorf("%d attempts of %s took %s — the fetch applied its own default (nostr.DefaultTimeout = %s), not the deadline it was given",
				attempts, perAttempt, elapsed, nostr.DefaultTimeout)
		}
		// And it really did WAIT: a fetch that gave up instantly would also be
		// fast, and would be a different bug.
		if elapsed < perAttempt {
			t.Errorf("%d attempts of %s returned in %s — the per-attempt deadline was never actually waited out", attempts, perAttempt, elapsed)
		}
	})

	t.Run("the command's own gather gets every attempt its budget is derived from", func(t *testing.T) {
		// portfolioGatherBudget's job is to bound the whole read WITHOUT
		// truncating the per-relay attempts inside it. The witness is what the
		// relay receives: if any layer applies a deadline longer than
		// portfolioRelayTimeout, the overall budget swallows attempt 1 whole and
		// attempt 2 never happens.
		portfolioEnv(t)
		shortRelayClock(t, 300*time.Millisecond)
		relay := newColdRelay(t, 99, nil)
		t.Setenv("RD_NOSTR_RELAY_URL", relay.url())

		out, _, err := tryBoardPortfolioCmd(t, true, false)
		if err == nil {
			t.Fatalf("a relay that never answered minted a link:\n%s", out)
		}
		if got := relay.receivedReqs(); got != portfolioRelayAttempts {
			t.Errorf("the relay received %d REQ(s) over the whole gather, want portfolioRelayAttempts = %d — the budget truncated the attempts it is derived from, or the fetch used a longer per-attempt deadline than %s",
				got, portfolioRelayAttempts, portfolioRelayTimeout)
		}
	})

	// The numbers themselves. These are the reason the per-attempt deadline is not
	// simply nostr.DefaultTimeout, and they are asserted directly because a change
	// to either silently changes what the tests above are measuring.
	if portfolioRelayTimeout <= nostr.DefaultTimeout {
		t.Errorf("portfolioRelayTimeout (%s) is no longer longer than nostr.DefaultTimeout (%s) — a scale-to-zero cold start past %s is the case this budget exists for",
			portfolioRelayTimeout, nostr.DefaultTimeout, nostr.DefaultTimeout)
	}
	if portfolioRelayAttempts < 2 {
		t.Errorf("portfolioRelayAttempts = %d — the attempt that times out is what wakes a cold relay, so there must be a second one", portfolioRelayAttempts)
	}
}

// TestBoardCmd_CompletenessFlagsRejectedWhereTheyClaimNothing: --allow-partial and
// --strict both move a completeness GUARANTEE, so each must be an error wherever
// no such guarantee is being made. Silently ignoring one would let a user believe
// they had opted into something they had not — the same shape of misplaced belief
// as the bug itself.
//
// ready-1df: the flags that make the claim changed (--portfolio/--with-key became
// the default; --no-key/--this-board are the ways to opt OUT of it), and the two
// completeness flags are now also mutually exclusive, so that pair is checked
// here too. The rejection must always name the flag it rejected, or the user
// cannot tell which half of their command was the problem.
func TestBoardCmd_CompletenessFlagsRejectedWhereTheyClaimNothing(t *testing.T) {
	cases := []struct {
		name  string
		flags boardFlags
		want  string
	}{
		{"--allow-partial with --no-key", boardFlags{allowPartial: true, noKey: true}, "--allow-partial"},
		{"--allow-partial with --this-board", boardFlags{allowPartial: true, thisBoard: true}, "--allow-partial"},
		{"--allow-partial with both", boardFlags{allowPartial: true, noKey: true, thisBoard: true}, "--allow-partial"},
		{"--strict with --no-key", boardFlags{strict: true, noKey: true}, "--strict"},
		{"--strict with --this-board", boardFlags{strict: true, thisBoard: true}, "--strict"},
		{"--strict with --allow-partial", boardFlags{strict: true, allowPartial: true}, "--strict"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			portfolioEnv(t)
			out, _, err := tryBoardCmd(t, c.flags)
			if err == nil {
				t.Fatalf("%s was accepted, where there is no completeness claim to change:\n%s", c.name, out)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("the rejection does not name %s; error = %q", c.want, err)
			}
			if strings.Contains(out, "#") {
				t.Errorf("a rejected invocation still printed a link:\n%s", out)
			}
		})
	}

	// ANTI-TAUTOLOGY: each completeness flag ALONE is accepted, so the rejections
	// above are about the COMBINATION and not about the flag existing.
	for _, c := range []struct {
		name  string
		flags boardFlags
	}{
		{"--allow-partial alone", boardFlags{allowPartial: true}},
		{"--strict alone", boardFlags{strict: true}},
	} {
		c := c
		t.Run(c.name+" is accepted", func(t *testing.T) {
			portfolioEnv(t) // no relays configured: nothing can fall short
			out, _, err := tryBoardCmd(t, c.flags)
			if err != nil {
				t.Fatalf("%s must be accepted on the link that makes the claim: %v", c.name, err)
			}
			if !strings.Contains(out, "#sk=") {
				t.Errorf("%s printed no portfolio link:\n%s", c.name, out)
			}
		})
	}
}

// TestBoardPortfolio_GatherFilterIsScopedToSelfNotEveryGrant asserts the QUERY
// portfolioGrantEvents sends, not what came back.
//
// Every relay fixture in this file (storingRelay, shortRelay, coldRelay) is
// filter-BLIND when serving: none of them reject or trim a REQ based on its
// filter, because correctness here is enforced client-side (Verify + the
// reconcile trust gate + DeriveBoardKeyring re-checking grantee/board), exactly
// as prod does against a real, untrusted relay. That means a portfolioGrantEvents
// defect that sent an over-broad filter — e.g. every kind instead of just
// KindRoleGrant, or no "#p" restriction at all (fetching every OTHER pubkey's
// grants too) — would still make every gather-completeness test in this file
// pass: the fixtures only ever seed events this key is SUPPOSED to see, so
// serving "everything" and serving "correctly filtered" look identical to any
// assertion keyed on the minted link's contents.
//
// This test closes that gap the other way: it inspects the actual REQ the
// gather sent and asserts its shape directly, independent of what the relay
// chose to answer with.
func TestBoardPortfolio_GatherFilterIsScopedToSelfNotEveryGrant(t *testing.T) {
	owner, _, _, _, dir, _, _, _, _ := portfolioEnv(t)
	relay := newStoringRelay(t)
	t.Cleanup(relay.close)
	setProjectRelays(t, dir, relay.url())

	out, errOut, err := tryBoardPortfolioCmd(t, true, false)
	if err != nil {
		t.Fatalf("rd board --portfolio --with-key: %v\nstderr:\n%s", err, errOut)
	}
	if !strings.Contains(out, "#sk=") {
		t.Fatalf("no portfolio link printed:\n%s", out)
	}

	// filterForKind, not lastFilter: `rd board --portfolio` also runs a separate
	// archived-boards gather (kinds=[KindBoard]) against the same relay, so the
	// LAST REQ sent is not necessarily the role-grant one this test is about.
	got := relay.filterForKind(rdSync.KindRoleGrant)
	if got == nil {
		t.Fatal("the gather never sent a kind-39301 (KindRoleGrant) REQ to the configured relay — nothing to assert the filter shape of")
	}
	if kinds := filterKinds(got); len(kinds) != 1 || kinds[0] != rdSync.KindRoleGrant {
		t.Errorf("portfolio gather filter kinds = %v, want exactly [%d] (KindRoleGrant) — an over-broad kind set would fetch board/card/status events too", kinds, rdSync.KindRoleGrant)
	}
	if p := filterStrings(got, "#p"); len(p) != 1 || p[0] != owner.PubKeyHex() {
		t.Errorf("portfolio gather filter #p = %v, want exactly [%q] — a missing or wrong #p would ask the relay for every OTHER pubkey's grants too", p, owner.PubKeyHex())
	}
	// Portfolio scope is intentionally NOT board-scoped: no "#a" tag, because the
	// whole point is every board this pubkey holds a grant for, not one.
	if a := filterStrings(got, "#a"); a != nil {
		t.Errorf("portfolio gather filter carries #a = %v — a portfolio-wide query must not be scoped to a single board coordinate", a)
	}
}
