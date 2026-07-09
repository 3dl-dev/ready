// NIP-77 negentropy transport (ready-797).
//
// Wraps the pure Negentropy V1 client (negentropy.go) in the NIP-77 relay
// message flow so rd can reconcile its local event-id set against a live strfry
// relay over a single websocket:
//
//	client -> ["NEG-OPEN", <sub>, <filter>, <hex initial msg>]
//	relay  -> ["NEG-MSG",  <sub>, <hex msg>]
//	client -> ["NEG-MSG",  <sub>, <hex msg>]   (repeat until reconciled)
//	client -> ["NEG-CLOSE", <sub>]
//
// The result is the DIFFERENCE between the two sets: Need = ids the relay has and
// the client lacks (download these), Have = ids the client has and the relay
// lacks (upload these). The bytes-on-the-wire are measured so callers can prove
// the transfer is bounded by the diff, not the whole set (the anti-fs-sync-
// pathology assertion). The actual event transfer is done by the caller via REQ
// (download) and EVENT (upload); this function only computes the diff.
package nostr

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/gorilla/websocket"
)

// NegSyncResult reports the outcome + measured cost of one negentropy session.
type NegSyncResult struct {
	// Need are event ids the relay has that the local set lacks (to download).
	Need []string
	// Have are event ids the local set has that the relay lacks (to upload).
	Have []string
	// BytesSent / BytesReceived count the negentropy MESSAGE bytes exchanged
	// (hex-decoded payloads, excluding websocket/JSON framing). This is the sync
	// cost — with negentropy it is bounded by the number of differing ids, NOT the
	// size of the set (the property that kills campfire's fs-sync re-sync).
	BytesSent     int
	BytesReceived int
	// RoundTrips is the number of NEG-MSG messages the client sent after NEG-OPEN.
	RoundTrips int
}

// NegentropyReconcile runs a NIP-77 negentropy reconciliation of localItems
// against the relay's set matching filter (a NIP-01 filter object, e.g.
// {"kinds":[30302],"#d":[itemID]}). It returns the have/need diff and the
// measured wire cost. rd is always the client/initiator; strfry is the server.
func NegentropyReconcile(ctx context.Context, relayURL string, filter map[string]any, localItems []NegItem) (NegSyncResult, error) {
	var res NegSyncResult

	neg, err := NewNegentropy(localItems)
	if err != nil {
		return res, err
	}

	conn, _, err := websocket.DefaultDialer.DialContext(ctx, relayURL, nil)
	if err != nil {
		return res, fmt.Errorf("nostr: neg dial %s: %w", relayURL, err)
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetWriteDeadline(dl)
		_ = conn.SetReadDeadline(dl)
	}

	const sub = "rd-neg"
	initial := neg.Initiate()
	res.BytesSent += len(initial)
	open := []any{"NEG-OPEN", sub, filter, hex.EncodeToString(initial)}
	if err := conn.WriteJSON(open); err != nil {
		return res, fmt.Errorf("nostr: write NEG-OPEN: %w", err)
	}

	// conn.ReadMessage() is not ctx-aware: if the caller's ctx carries no
	// Deadline (so SetReadDeadline above was a no-op) and cancels ctx, a
	// direct blocking read here would ignore that cancellation until the
	// websocket otherwise errors (relay hangs, network drops, etc). Run the
	// read in a goroutine and select on ctx.Done() so cancellation always
	// unblocks the loop promptly. Closing conn on cancellation also unsticks
	// the goroutine's in-flight ReadMessage so it can exit instead of leaking.
	type negFrame struct {
		data []byte
		err  error
	}
	readCh := make(chan negFrame, 1)
	go func() {
		for {
			_, data, err := conn.ReadMessage()
			select {
			case readCh <- negFrame{data: data, err: err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	readFrame := func() ([]byte, error) {
		select {
		case f := <-readCh:
			return f.data, f.err
		case <-ctx.Done():
			_ = conn.Close()
			return nil, ctx.Err()
		}
	}

	haveSet := map[string]bool{}
	needSet := map[string]bool{}

	for {
		data, err := readFrame()
		if err != nil {
			return res, fmt.Errorf("nostr: neg read: %w", err)
		}
		var frame []json.RawMessage
		if err := json.Unmarshal(data, &frame); err != nil || len(frame) == 0 {
			continue
		}
		var typ string
		if err := json.Unmarshal(frame[0], &typ); err != nil {
			continue
		}
		switch typ {
		case "NEG-MSG":
			if len(frame) < 3 {
				continue
			}
			var gotSub, hexMsg string
			_ = json.Unmarshal(frame[1], &gotSub)
			if gotSub != sub {
				continue
			}
			_ = json.Unmarshal(frame[2], &hexMsg)
			msg, err := hex.DecodeString(hexMsg)
			if err != nil {
				return res, fmt.Errorf("nostr: decode NEG-MSG: %w", err)
			}
			res.BytesReceived += len(msg)

			response, have, need, err := neg.Reconcile(msg)
			if err != nil {
				return res, err
			}
			for _, id := range have {
				haveSet[HexID(id)] = true
			}
			for _, id := range need {
				needSet[HexID(id)] = true
			}
			if response == nil {
				// Reconciled — tell the relay we are done.
				_ = conn.WriteJSON([]any{"NEG-CLOSE", sub})
				res.Have = keysOf(haveSet)
				res.Need = keysOf(needSet)
				return res, nil
			}
			res.BytesSent += len(response)
			res.RoundTrips++
			if err := conn.WriteJSON([]any{"NEG-MSG", sub, hex.EncodeToString(response)}); err != nil {
				return res, fmt.Errorf("nostr: write NEG-MSG: %w", err)
			}
		case "NEG-ERR":
			var gotSub, reason string
			if len(frame) >= 2 {
				_ = json.Unmarshal(frame[1], &gotSub)
			}
			if len(frame) >= 3 {
				_ = json.Unmarshal(frame[2], &reason)
			}
			return res, fmt.Errorf("nostr: relay NEG-ERR: %s", reason)
		default:
			// NOTICE or unrelated frame — ignore.
			continue
		}
	}
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
