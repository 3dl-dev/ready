package sync

// VerifyEventsOnRelay — the narrow read-back a key rotation depends on (ready-2b25).
//
// The whole point of this function is that it must not be fooled. rd's own publish
// report says "accepted" as soon as ANY relay accepts, so this is the only evidence
// that a particular relay serves a particular grant. The WEAKEST thing that could
// satisfy "the relay answered with event X" is a relay echoing back a fabricated
// blob labelled with X's id — so the tampering case below is the load-bearing test,
// not the happy path.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/gorilla/websocket"
)

// idServingRelay is a minimal in-process NIP-01 relay that answers a REQ's "ids"
// filter from a fixed set of events (whatever those events happen to contain —
// including deliberately corrupted ones) and then sends EOSE. `volunteer` is sent
// on every REQ regardless of the filter, modelling a relay that serves more than
// it was asked for.
func idServingRelay(t *testing.T, held []*nostr.Event, volunteer []*nostr.Event) string {
	t.Helper()
	byID := map[string]*nostr.Event{}
	for _, e := range held {
		byID[e.ID] = e
	}
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
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
			var typ, sub string
			_ = json.Unmarshal(frame[0], &typ)
			_ = json.Unmarshal(frame[1], &sub)
			if typ != "REQ" {
				continue
			}
			var filter struct {
				IDs []string `json:"ids"`
			}
			if len(frame) >= 3 {
				_ = json.Unmarshal(frame[2], &filter)
			}
			for _, id := range filter.IDs {
				if e, ok := byID[id]; ok {
					_ = conn.WriteJSON([]any{"EVENT", sub, e})
				}
			}
			for _, e := range volunteer {
				_ = conn.WriteJSON([]any{"EVENT", sub, e})
			}
			_ = conn.WriteJSON([]any{"EOSE", sub})
		}
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

// signedGrants mints n owner-signed kind-39301 grants for a throwaway board.
func signedGrants(t *testing.T, n int) (*nostr.Key, []*nostr.Event) {
	t.Helper()
	owner, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	out := make([]*nostr.Event, 0, n)
	for i := 0; i < n; i++ {
		grantee, gerr := nostr.GenerateKey()
		if gerr != nil {
			t.Fatalf("GenerateKey grantee: %v", gerr)
		}
		ev, berr := BuildRoleGrantEvent(owner, RoleGrantSpec{
			BoardD: "readback", BoardAuthor: owner.PubKeyHex(), Grantee: grantee.PubKeyHex(),
			Role: RoleContributor, Label: "member", WrappedCEK: "wrap", CEKEpoch: 2, WrappedLTK: "ltk",
		}, int64(1700000000+i))
		if berr != nil {
			t.Fatalf("BuildRoleGrantEvent: %v", berr)
		}
		out = append(out, ev)
	}
	return owner, out
}

func TestVerifyEventsOnRelay_AllPresent(t *testing.T) {
	_, grants := signedGrants(t, 3)
	relay := idServingRelay(t, grants, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rb, err := VerifyEventsOnRelay(ctx, relay, grants)
	if err != nil {
		t.Fatalf("VerifyEventsOnRelay: %v", err)
	}
	if !rb.Match {
		t.Fatalf("Match=false for a relay holding every event: %+v", rb)
	}
	if rb.Want != 3 || len(rb.Present) != 3 || len(rb.Missing) != 0 {
		t.Fatalf("want=3 present=%d missing=%d, want 3/0: %+v", len(rb.Present), len(rb.Missing), rb)
	}
}

func TestVerifyEventsOnRelay_MissingIsReported(t *testing.T) {
	_, grants := signedGrants(t, 3)
	// The relay holds only the first two — the third never landed.
	relay := idServingRelay(t, grants[:2], nil)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rb, err := VerifyEventsOnRelay(ctx, relay, grants)
	if err != nil {
		t.Fatalf("VerifyEventsOnRelay: %v", err)
	}
	if rb.Match {
		t.Fatal("Match=true although the relay serves only 2 of 3 grants — a rotation would be reported as visible when a member cannot see its key")
	}
	if len(rb.Missing) != 1 || rb.Missing[0] != grants[2].ID {
		t.Fatalf("Missing=%v, want exactly [%s]", rb.Missing, grants[2].ID)
	}
	if len(rb.Present) != 2 {
		t.Fatalf("Present=%d, want 2", len(rb.Present))
	}
}

// TestVerifyEventsOnRelay_TamperedAnswerIsNotEvidence is the assertion that makes
// the whole read-back worth running. A relay that answers a REQ for id X with a
// blob merely LABELLED X — different content, different tags, no valid signature
// over those bytes — must be treated as "X is not there", not as proof it is. If
// this passed, a hostile or buggy relay could report a rotation as fully
// distributed while serving members nothing they can use.
func TestVerifyEventsOnRelay_TamperedAnswerIsNotEvidence(t *testing.T) {
	_, grants := signedGrants(t, 2)
	// Keep the claimed id, corrupt the signed bytes: Verify re-derives the id from
	// the canonical serialization, so this no longer hashes to the id it claims.
	tampered := *grants[1]
	tampered.Content = "attacker-substituted content"
	relay := idServingRelay(t, []*nostr.Event{grants[0], &tampered}, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rb, err := VerifyEventsOnRelay(ctx, relay, grants)
	if err != nil {
		t.Fatalf("VerifyEventsOnRelay: %v", err)
	}
	if rb.Match {
		t.Fatal("Match=true although the relay answered one id with an event whose bytes do not verify")
	}
	if len(rb.Unverified) != 1 || rb.Unverified[0] != grants[1].ID {
		t.Fatalf("Unverified=%v, want exactly [%s]", rb.Unverified, grants[1].ID)
	}
	// A tampered answer must ALSO count as missing: the real event is not
	// retrievable from that relay.
	if len(rb.Missing) != 1 || rb.Missing[0] != grants[1].ID {
		t.Fatalf("Missing=%v, want the tampered id [%s] counted as missing", rb.Missing, grants[1].ID)
	}
	for _, id := range rb.Present {
		if id == grants[1].ID {
			t.Fatal("a tampered answer was recorded as PRESENT")
		}
	}
}

// TestVerifyEventsOnRelay_IgnoresUnrequestedEvents: a relay that floods the
// subscription with other events must not inflate the result. Present counts only
// ids that were asked for.
func TestVerifyEventsOnRelay_IgnoresUnrequestedEvents(t *testing.T) {
	_, grants := signedGrants(t, 2)
	_, noise := signedGrants(t, 5)
	relay := idServingRelay(t, grants, noise)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rb, err := VerifyEventsOnRelay(ctx, relay, grants)
	if err != nil {
		t.Fatalf("VerifyEventsOnRelay: %v", err)
	}
	if !rb.Match || len(rb.Present) != 2 || rb.Want != 2 {
		t.Fatalf("want exactly the 2 requested ids present, got %+v", rb)
	}
	asked := map[string]bool{grants[0].ID: true, grants[1].ID: true}
	for _, id := range rb.Present {
		if !asked[id] {
			t.Fatalf("Present contains id %s that was never requested", id)
		}
	}
}

// TestVerifyEventsOnRelay_UnreachableRelayIsAnError: a relay that cannot be dialed
// must surface as an error, never as an empty-but-matching read-back.
func TestVerifyEventsOnRelay_UnreachableRelayIsAnError(t *testing.T) {
	_, grants := signedGrants(t, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	rb, err := VerifyEventsOnRelay(ctx, "ws://127.0.0.1:1", grants)
	if err == nil {
		t.Fatalf("expected an error dialing an unreachable relay, got %+v", rb)
	}
	if rb.Match {
		t.Fatal("Match=true on a failed read — an unreachable relay must never look like a pass")
	}
}
