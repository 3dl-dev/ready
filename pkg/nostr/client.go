package nostr

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gorilla/websocket"
)

// Publish opens a websocket connection to a single relay, sends the event as a
// NIP-01 ["EVENT", <event>] message, and waits for the relay's
// ["OK", <id>, <accepted>, <message>] reply. It returns accepted=true only when
// the relay reports OK,true for THIS event id. This is the live relay-accept
// gate: a real strfry either accepts a well-formed signed event or rejects it
// with a reason.
func Publish(ctx context.Context, relayURL string, e *Event) (accepted bool, message string, err error) {
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, relayURL, nil)
	if err != nil {
		return false, "", fmt.Errorf("nostr: dial %s: %w", relayURL, err)
	}
	defer conn.Close()

	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetWriteDeadline(dl)
		_ = conn.SetReadDeadline(dl)
	}

	envelope := []any{"EVENT", e}
	if err := conn.WriteJSON(envelope); err != nil {
		return false, "", fmt.Errorf("nostr: write EVENT: %w", err)
	}

	// Read frames until we see the OK for our id (relays may interleave NOTICE
	// or other frames).
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return false, "", fmt.Errorf("nostr: read OK: %w", err)
		}
		var frame []json.RawMessage
		if err := json.Unmarshal(data, &frame); err != nil || len(frame) == 0 {
			continue
		}
		var typ string
		if err := json.Unmarshal(frame[0], &typ); err != nil {
			continue
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
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, relayURL, nil)
	if err != nil {
		return nil, fmt.Errorf("nostr: dial %s: %w", relayURL, err)
	}
	defer conn.Close()

	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetWriteDeadline(dl)
		_ = conn.SetReadDeadline(dl)
	}

	const sub = "rd-fetch"
	req := []any{"REQ", sub, map[string]any{"ids": []string{id}}}
	if err := conn.WriteJSON(req); err != nil {
		return nil, fmt.Errorf("nostr: write REQ: %w", err)
	}

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return nil, fmt.Errorf("nostr: read EVENT: %w", err)
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

func writeClose(conn *websocket.Conn, sub string) error {
	return conn.WriteJSON([]any{"CLOSE", sub})
}

// DefaultTimeout is a reasonable per-relay operation deadline for LAN relays.
const DefaultTimeout = 10 * time.Second
