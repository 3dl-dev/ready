package nostr

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gorilla/websocket"
)

// dialRelay opens a websocket connection to relayURL and — when ctx carries a
// deadline — arms BOTH the write and read deadlines to it. Every relay op
// (Publish/Fetch/FetchMany) hand-rolled this identical dial+arm sequence; this
// is the single shared entry point so a change to dial behavior (e.g. TLS
// config) only needs to happen once.
func dialRelay(ctx context.Context, relayURL string) (*websocket.Conn, error) {
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, relayURL, nil)
	if err != nil {
		return nil, fmt.Errorf("nostr: dial %s: %w", relayURL, err)
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetWriteDeadline(dl)
		_ = conn.SetReadDeadline(dl)
	}
	return conn, nil
}

// readNIP01Frame reads one raw relay frame and decodes its NIP-01 envelope type
// (frame[0], e.g. "EVENT"/"OK"/"EOSE"/"CLOSED"). frame is the full decoded array
// for the caller's field-specific unmarshaling; typ is "" whenever the frame
// fails to parse (malformed JSON, empty array, or a non-string first element) —
// matching the original per-call behavior of every relay op, which silently
// skips such frames and keeps reading (callers loop again on typ == "").
// err is non-nil only on a transport-level ReadMessage failure.
func readNIP01Frame(conn *websocket.Conn) (typ string, frame []json.RawMessage, err error) {
	_, data, err := conn.ReadMessage()
	if err != nil {
		return "", nil, err
	}
	if err := json.Unmarshal(data, &frame); err != nil || len(frame) == 0 {
		return "", nil, nil
	}
	if err := json.Unmarshal(frame[0], &typ); err != nil {
		return "", nil, nil
	}
	return typ, frame, nil
}

// Publish opens a websocket connection to a single relay, sends the event as a
// NIP-01 ["EVENT", <event>] message, and waits for the relay's
// ["OK", <id>, <accepted>, <message>] reply. It returns accepted=true only when
// the relay reports OK,true for THIS event id. This is the live relay-accept
// gate: a real strfry either accepts a well-formed signed event or rejects it
// with a reason.
func Publish(ctx context.Context, relayURL string, e *Event) (accepted bool, message string, err error) {
	conn, err := dialRelay(ctx, relayURL)
	if err != nil {
		return false, "", err
	}
	defer conn.Close()

	envelope := []any{"EVENT", e}
	if err := conn.WriteJSON(envelope); err != nil {
		return false, "", fmt.Errorf("nostr: write EVENT: %w", err)
	}

	// Read frames until we see the OK for our id (relays may interleave NOTICE
	// or other frames).
	for {
		typ, frame, err := readNIP01Frame(conn)
		if err != nil {
			return false, "", fmt.Errorf("nostr: read OK: %w", err)
		}
		if typ != "OK" || len(frame) < 3 {
			continue
		}
		var id string
		_ = json.Unmarshal(frame[1], &id)
		if id != e.ID {
			continue
		}
		var ok bool
		_ = json.Unmarshal(frame[2], &ok)
		var msg string
		if len(frame) >= 4 {
			_ = json.Unmarshal(frame[3], &msg)
		}
		return ok, msg, nil
	}
}

// Fetch opens a websocket connection to a single relay and requests the event
// with the given id via a NIP-01 ["REQ", <sub>, {"ids":[id]}] subscription. It
// returns the first matching event the relay serves, or an error if none
// arrives before EOSE / the context deadline. This is the independent read-back
// used to prove the relay actually stored the event and to run Verify against a
// round-tripped copy.
func Fetch(ctx context.Context, relayURL, id string) (*Event, error) {
	conn, err := dialRelay(ctx, relayURL)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	const sub = "rd-fetch"
	req := []any{"REQ", sub, map[string]any{"ids": []string{id}}}
	if err := conn.WriteJSON(req); err != nil {
		return nil, fmt.Errorf("nostr: write REQ: %w", err)
	}

	for {
		typ, frame, err := readNIP01Frame(conn)
		if err != nil {
			return nil, fmt.Errorf("nostr: read EVENT: %w", err)
		}
		switch typ {
		case "EVENT":
			if len(frame) < 3 {
				continue
			}
			var got Event
			if err := json.Unmarshal(frame[2], &got); err != nil {
				continue
			}
			if got.ID != id {
				continue
			}
			_ = writeClose(conn, sub)
			return &got, nil
		case "EOSE":
			_ = writeClose(conn, sub)
			return nil, fmt.Errorf("nostr: event %s not found on %s (EOSE)", id, relayURL)
		}
	}
}

// FetchMany opens a websocket connection to a single relay and runs a NIP-01
// ["REQ", <sub>, <filter>] subscription, collecting every EVENT the relay serves
// until EOSE (or the context deadline). filter is a raw NIP-01 filter object,
// e.g. {"kinds":[30302],"authors":[pk],"#d":[itemID]}. It returns the events in
// relay-delivery order. This is the cache-fill/reconcile primitive: rd queries a
// relay for an item's card + status events, then merges them into the local
// authoritative log — the relay is never the authority.
func FetchMany(ctx context.Context, relayURL string, filter map[string]any) ([]*Event, error) {
	conn, err := dialRelay(ctx, relayURL)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	const sub = "rd-fetchmany"
	req := []any{"REQ", sub, filter}
	if err := conn.WriteJSON(req); err != nil {
		return nil, fmt.Errorf("nostr: write REQ: %w", err)
	}

	var out []*Event
	for {
		typ, frame, err := readNIP01Frame(conn)
		if err != nil {
			return nil, fmt.Errorf("nostr: read EVENT: %w", err)
		}
		switch typ {
		case "EVENT":
			if len(frame) < 3 {
				continue
			}
			var got Event
			if err := json.Unmarshal(frame[2], &got); err != nil {
				continue
			}
			ev := got
			out = append(out, &ev)
		case "EOSE":
			_ = writeClose(conn, sub)
			return out, nil
		case "CLOSED":
			// The relay refused the subscription (e.g. an oversized/invalid filter).
			// NIP-01: ["CLOSED", <sub>, <message>]. Surface it as an error instead of
			// blocking on ReadMessage until the deadline fires as a silent i/o timeout.
			var msg string
			if len(frame) >= 3 {
				_ = json.Unmarshal(frame[2], &msg)
			}
			return nil, fmt.Errorf("nostr: relay %s closed subscription: %q", relayURL, msg)
		}
	}
}

// MaxREQIDs bounds how many event ids rd puts in a single NIP-01 REQ filter.
// strfry (and relays generally) reject or silently drop a REQ whose ids filter is
// too large: a single REQ for ~9k ids against a locked strfry returns NO frames at
// all — not even EOSE — so FetchMany blocks until its read deadline and fails with
// a bare "i/o timeout" (ready-8de). Chunking the id set into REQs of this size
// downloads the same events reliably in a fraction of a second. 500 matches
// strfry's default maxFilterLimit and was proven to return the full set fast
// against the live locked relays.
const MaxREQIDs = 500

// FetchByIDs downloads the events with the given ids from one relay, chunking the
// id set into REQs of at most MaxREQIDs so a large `need` set (e.g. a fresh
// machine's negentropy download of a whole board) does not overflow the relay's
// per-REQ filter limit. It opens a fresh subscription per chunk (FetchMany), and
// returns every event served across all chunks in delivery order. Duplicate ids
// across chunks cannot occur because the input ids are partitioned; the caller is
// responsible for admission (Verify + trust) and dedupe on merge.
//
// The whole operation shares ctx's deadline: each chunk reuses the same context,
// so the total download is bounded by ctx, not by (chunks * per-chunk timeout).
func FetchByIDs(ctx context.Context, relayURL string, ids []string) ([]*Event, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var out []*Event
	for start := 0; start < len(ids); start += MaxREQIDs {
		end := start + MaxREQIDs
		if end > len(ids) {
			end = len(ids)
		}
		batch, err := FetchMany(ctx, relayURL, map[string]any{"ids": ids[start:end]})
		if err != nil {
			return nil, fmt.Errorf("nostr: fetch ids [%d:%d] of %d from %s: %w", start, end, len(ids), relayURL, err)
		}
		out = append(out, batch...)
	}
	return out, nil
}

func writeClose(conn *websocket.Conn, sub string) error {
	return conn.WriteJSON([]any{"CLOSE", sub})
}

// DefaultTimeout is a reasonable per-relay operation deadline for LAN relays.
const DefaultTimeout = 10 * time.Second
