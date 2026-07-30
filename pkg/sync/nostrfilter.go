// NIP-01 filter matching (ready-797).
//
// matchesFilter evaluates a nostr event against the subset of NIP-01 filter
// fields rd's negentropy sync uses: kinds, authors, ids, and "#<tag>" single-
// letter tag filters. It mirrors how the relay selects its record set for a
// NEG-OPEN filter, so the LOCAL log is reduced to the same universe the relay
// reconciles over — otherwise the have/need diff would be skewed. A filter field
// that is absent matches everything; within a field the values are ORed; across
// fields they are ANDed (NIP-01 semantics).
package sync

import "github.com/3dl-dev/ready/pkg/nostr"

func matchesFilter(e *nostr.Event, filter map[string]any) bool {
	for key, raw := range filter {
		switch key {
		case "kinds":
			if !kindMatches(e.Kind, raw) {
				return false
			}
		case "authors":
			if !stringInField(e.PubKey, raw) {
				return false
			}
		case "ids":
			if !stringInField(e.ID, raw) {
				return false
			}
		case "until":
			// NIP-01: created_at <= until (INCLUSIVE). The negentropy paging walk
			// (ready-bec) asks the relay for one time window at a time; the LOCAL set
			// must be reduced to the SAME window or the diff is skewed — every local
			// event newer than the window would come back as "the relay lacks this"
			// and be re-uploaded on every sync.
			ts, ok := timestampField(raw)
			if !ok || e.CreatedAt > ts {
				return false
			}
		case "since":
			// NIP-01: created_at >= since (INCLUSIVE). rd builds no since-scoped
			// filter today; matching it correctly costs one branch and stops a future
			// one from silently degrading to "matches everything".
			ts, ok := timestampField(raw)
			if !ok || e.CreatedAt < ts {
				return false
			}
		default:
			if len(key) == 2 && key[0] == '#' {
				if !tagMatches(e, key[1], raw) {
					return false
				}
			}
			// Unknown keys (e.g. limit) are ignored for local matching: `limit` is a
			// relay-side cap on how many of the matching events are SERVED, not a
			// predicate on an event, so applying it locally would be meaningless.
		}
	}
	return true
}

func kindMatches(kind int, raw any) bool {
	switch v := raw.(type) {
	case []int:
		for _, k := range v {
			if k == kind {
				return true
			}
		}
	case []any:
		for _, e := range v {
			switch n := e.(type) {
			case int:
				if n == kind {
					return true
				}
			case float64:
				if int(n) == kind {
					return true
				}
			}
		}
	}
	return false
}

func stringInField(s string, raw any) bool {
	switch v := raw.(type) {
	case []string:
		for _, x := range v {
			if x == s {
				return true
			}
		}
	case []any:
		for _, e := range v {
			if x, ok := e.(string); ok && x == s {
				return true
			}
		}
	}
	return false
}

// timestampField reads a NIP-01 since/until value, which may arrive as any of the
// numeric shapes a Go map or a JSON decode produces. A value that is not a number
// is NOT treated as "absent": the caller fails the match, so a malformed filter
// selects nothing locally rather than silently widening to everything.
func timestampField(raw any) (int64, bool) {
	switch v := raw.(type) {
	case int64:
		return v, true
	case int:
		return int64(v), true
	case float64:
		return int64(v), true
	case uint64:
		return int64(v), true
	}
	return 0, false
}

// tagMatches reports whether the event has a tag whose name is the single letter
// `letter` and whose value is among the filter's values.
func tagMatches(e *nostr.Event, letter byte, raw any) bool {
	name := string(letter)
	for _, tag := range e.Tags {
		if len(tag) >= 2 && tag[0] == name {
			if stringInField(tag[1], raw) {
				return true
			}
		}
	}
	return false
}
