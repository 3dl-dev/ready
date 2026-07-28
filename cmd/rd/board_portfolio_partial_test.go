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
// WHAT IS ASSERTED HERE, and why each one is a separate test:
//
//	1. An incomplete gather REFUSES to mint (Refuses...): no URL on stdout, a
//	   non-zero return, and an error naming the relay and the way through.
//	2. --allow-partial mints, and the warning it prints never says "entire
//	   portfolio" (AllowPartial...): the count and the claim must agree.
//	3. A COMPLETE gather still says "ENTIRE PORTFOLIO" (CompleteGather...): a gate
//	   that fired on every read would pass (1) and (2) and destroy the feature.
//	4. A cold relay is RETRIED and the link is complete when the retry lands
//	   (ColdRelay...), and reported as a shortfall when it does not.
//	5. relayFetchMany can SEE a partial read where followFetch structurally
//	   cannot (RelayFetchMany...) — the fix is at the fetch, not only at the one
//	   caller that got bitten.
//	6. The gather budget covers the per-relay attempts it contains
//	   (GatherBudget...) — the invariant whose violation made the old comment
//	   false.
//
// Every relay here is a REAL in-process NIP-01 relay (storingRelay, or the
// stalling variant below) reached over a real websocket by the real
// nostr.FetchMany. Only the clock is shortened.

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
	srv    *httptest.Server
	mu     sync.Mutex
	stall  int // REQs still to be swallowed
	served int // REQs actually answered
}

func newColdRelay(t *testing.T, stall int, events []*nostr.Event) *coldRelay {
	t.Helper()
	r := &coldRelay{stall: stall}
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
			cold := r.stall > 0
			if cold {
				r.stall--
			} else {
				r.served++
			}
			r.mu.Unlock()
			if cold {
				// Asleep: no EVENT, no EOSE, no error. The caller's per-attempt
				// deadline is the only thing that ends this.
				continue
			}
			var sub string
			_ = json.Unmarshal(frame[1], &sub)
			for _, e := range events {
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
	// And it must never carry the lie forward into the failure text.
	if strings.Contains(strings.ToLower(msg), "entire portfolio") {
		t.Errorf("the refusal claims 'entire portfolio' about a set it could not confirm; error = %q", msg)
	}
}

// TestBoardPortfolio_AllowPartial_NeverClaimsTheEntirePortfolio is requirement 2
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

// TestBoardPortfolio_CompleteGather_StillClaimsTheEntirePortfolio is the
// anti-overfire control. A gate that refused (or downgraded the wording) on every
// read would satisfy both tests above and quietly delete the feature, so the
// complete path is asserted to be UNCHANGED — with a live relay actually
// answering, not merely with no relay configured.
func TestBoardPortfolio_CompleteGather_StillClaimsTheEntirePortfolio(t *testing.T) {
	portfolioEnv(t)
	relay := newStoringRelay(t)
	t.Cleanup(relay.close)
	t.Setenv("RD_NOSTR_RELAY_URL", relay.url())

	out, errOut, err := tryBoardPortfolioCmd(t, true, false)
	if err != nil {
		t.Fatalf("a reachable relay must not trip the completeness gate: %v", err)
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
}

// TestBoardPortfolio_ColdRelayIsRetried covers requirement 3 in both directions:
// a relay that wakes up on the retry yields a COMPLETE link, and a relay that
// never wakes is reported as a shortfall instead of being absorbed.
func TestBoardPortfolio_ColdRelayIsRetried(t *testing.T) {
	t.Run("wakes on the retry -> complete link", func(t *testing.T) {
		portfolioEnv(t)
		shortRelayClock(t, 300*time.Millisecond)
		relay := newColdRelay(t, 1, nil) // asleep for exactly one REQ
		t.Setenv("RD_NOSTR_RELAY_URL", relay.url())

		out, errOut, err := tryBoardPortfolioCmd(t, true, false)
		if err != nil {
			t.Fatalf("a relay that woke on attempt 2 must produce a complete link, not a refusal: %v", err)
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

// TestPortfolioGatherBudget_CoversItsPerRelayAttempts is requirement 4 made
// executable. The old code documented a 90s budget "because the relay is
// scale-to-zero" while capping every relay at nostr.DefaultTimeout inside it, so
// the 90s could never reach a cold start. A comment cannot be tested; this
// invariant can, and it is the one the comment was asserting.
func TestPortfolioGatherBudget_CoversItsPerRelayAttempts(t *testing.T) {
	for _, n := range []int{0, 1, 2, 3, 7} {
		relays := n
		if relays < 1 {
			relays = 1
		}
		want := time.Duration(relays) * time.Duration(portfolioRelayAttempts) * portfolioRelayTimeout
		if got := portfolioGatherBudget(n); got < want {
			t.Errorf("portfolioGatherBudget(%d) = %s, which truncates its own per-relay budget (%d relays x %d attempts x %s = %s)",
				n, got, relays, portfolioRelayAttempts, portfolioRelayTimeout, want)
		}
	}
	if portfolioRelayTimeout <= nostr.DefaultTimeout {
		t.Errorf("portfolioRelayTimeout (%s) is no longer longer than nostr.DefaultTimeout (%s) — a scale-to-zero cold start past %s is the case this budget exists for",
			portfolioRelayTimeout, nostr.DefaultTimeout, nostr.DefaultTimeout)
	}
	if portfolioRelayAttempts < 2 {
		t.Errorf("portfolioRelayAttempts = %d — the attempt that times out is what wakes a cold relay, so there must be a second one", portfolioRelayAttempts)
	}
}

// TestBoardPortfolio_AllowPartialRejectedWhereItClaimsNothing: the flag relaxes a
// completeness guarantee, so it must be an error wherever no such guarantee is
// being made. Silently ignoring it would let a user believe they had opted into
// something they had not — the same shape of misplaced belief as the bug itself.
func TestBoardPortfolio_AllowPartialRejectedWhereItClaimsNothing(t *testing.T) {
	cases := []struct{ portfolio, withKey bool }{
		{false, false},
		{false, true},
		{true, false},
	}
	for _, c := range cases {
		portfolioEnv(t)
		setFlag := func(name, value string) {
			if err := boardCmd.Flags().Set(name, value); err != nil {
				t.Fatalf("set --%s: %v", name, err)
			}
		}
		setFlag("allow-partial", "true")
		setFlag("portfolio", boolStr(c.portfolio))
		setFlag("with-key", boolStr(c.withKey))

		var err error
		out := captureStdoutPipe(t, func() { err = boardCmd.RunE(boardCmd, nil) })

		_ = boardCmd.Flags().Set("allow-partial", "false")
		_ = boardCmd.Flags().Set("portfolio", "false")
		_ = boardCmd.Flags().Set("with-key", "false")

		if err == nil {
			t.Errorf("--allow-partial was accepted with --portfolio=%v --with-key=%v, where there is no completeness claim to relax:\n%s", c.portfolio, c.withKey, out)
			continue
		}
		if !strings.Contains(err.Error(), "--allow-partial") {
			t.Errorf("the rejection does not name the flag it rejected; error = %q", err)
		}
	}
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
