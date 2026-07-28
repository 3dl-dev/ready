package main

// ready-4d9 (follow-up): WHICH RELAYS ANSWERED — not just what came back.
//
// THE DEFECT THIS FILE EXISTS TO REMOVE. followFetch merged N relays into one
// event slice and returned an error only when the merge came back EMPTY. That
// reduction — "any relay accepts, so the read succeeded" — is this project's
// recurring defect shape (pkg/sync/relayclass.go reduceEventOutcome; ready-f7b;
// ready-260) and it is what produced this item's own bug: with a populated local
// log and an unreachable relay, `rd board --portfolio --with-key` minted a link
// covering ONE board out of 47 and told the owner, in capitals, that it carried
// the read keys for their ENTIRE PORTFOLIO. The loss was not merely unhandled at
// the call site — it was UNDETECTABLE there, because nothing in followFetch's two
// return values could express "three relays were asked, one answered".
//
// So the fix is here, at the fetch, and not only at the one caller that got
// bitten. relayFetchMany reports the per-relay outcome. A caller that cannot
// tolerate a partial read (a whole-portfolio bearer credential: the link is only
// honest if the board set behind it is complete) can now refuse. A caller that
// CAN tolerate one (rd follow's best-effort board backfill) keeps its previous
// behaviour unchanged, because followFetch is now defined in terms of this
// function and preserves its old contract exactly.
//
// RETRIES AND DEADLINES ARE PER-ATTEMPT, AND THE CALLER SETS THEM. The public
// relay is minReplicas=0 scale-to-zero; a cold start has been observed past 12s,
// which nostr.DefaultTimeout (10s) cannot absorb. A gather that must be complete
// therefore needs both a longer per-attempt deadline and a second attempt — the
// first attempt is what WAKES the relay, so the retry is the one that lands. The
// per-attempt deadline is always clamped by the caller's ctx, so an overall
// budget still bounds the whole thing.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/3dl-dev/ready/pkg/nostr"
)

// relayFailure names one relay that never answered, and why. The URL is carried
// so the operator is told WHICH relay to bring up rather than "a relay failed".
type relayFailure struct {
	Relay string
	Err   error
}

// relayFetchOutcome is the FULL result of a multi-relay read: the merged,
// de-duplicated events AND the identity of every relay that did not answer.
//
// Answered and Failed together account for every relay that was asked, so
// `len(Failed) == 0` is a positive statement — "every relay we asked answered" —
// rather than the absence of an error. That distinction is the whole point of the
// type: an empty error is not evidence of a complete read.
type relayFetchOutcome struct {
	Events   []*nostr.Event
	Answered []string
	Failed   []relayFailure
}

// complete reports that every relay asked actually answered. Asking ZERO relays
// (a local-only project, or a filter run with no read relays configured) is
// complete too: there is no relay whose contents were missed.
func (o relayFetchOutcome) complete() bool { return len(o.Failed) == 0 }

// shortfall renders the unanswered relays for an operator-facing message:
//
//	wss://relay.example (dial tcp ...: connection refused)
//
// It is "" when the read was complete, so a caller can use it directly as the
// reason clause of an error without re-testing completeness.
func (o relayFetchOutcome) shortfall() string {
	parts := make([]string, 0, len(o.Failed))
	for _, f := range o.Failed {
		parts = append(parts, fmt.Sprintf("%s (%v)", f.Relay, f.Err))
	}
	return strings.Join(parts, "; ")
}

// firstErr is the first relay error, for callers that only propagate one.
func (o relayFetchOutcome) firstErr() error {
	if len(o.Failed) == 0 {
		return nil
	}
	return o.Failed[0].Err
}

// relayFetchOpts tunes one multi-relay read. The zero value reproduces the
// historical followFetch behaviour exactly: one attempt per relay, capped at
// nostr.DefaultTimeout.
type relayFetchOpts struct {
	// PerAttempt is the deadline for ONE attempt against ONE relay. 0 means
	// nostr.DefaultTimeout. It is always further clamped by the caller's ctx.
	PerAttempt time.Duration
	// Attempts is the number of tries per relay. <2 means a single try. A retry
	// exists for the scale-to-zero cold start: the failed first attempt is what
	// woke the relay.
	Attempts int
	// OnRetry, when set, is called before each retry so an interactive command
	// can tell the operator why it is still waiting instead of appearing hung.
	OnRetry func(relay string, nextAttempt int, err error)
}

func (o relayFetchOpts) perAttempt() time.Duration {
	if o.PerAttempt <= 0 {
		return nostr.DefaultTimeout
	}
	return o.PerAttempt
}

func (o relayFetchOpts) attempts() int {
	if o.Attempts < 1 {
		return 1
	}
	return o.Attempts
}

// relayFetchMany queries every relay for filter and reports, per relay, whether
// it answered. It is a package-level var for the same no-live-relay test seam
// reason followFetch is one.
//
// A relay that errors on every attempt lands in Failed and its events are simply
// absent; a relay that answers with zero events is a SUCCESS (the relay is up and
// genuinely holds nothing), which is precisely the distinction the old boolean
// "did anything come back" could not draw.
var relayFetchMany = func(ctx context.Context, relays []string, filter map[string]any, opts relayFetchOpts) relayFetchOutcome {
	seen := map[string]*nostr.Event{}
	out := relayFetchOutcome{}

	for _, r := range relays {
		var lastErr error
		ok := false
		for attempt := 1; attempt <= opts.attempts(); attempt++ {
			if err := ctx.Err(); err != nil {
				// The overall budget is spent. Record it against this relay
				// rather than pretending the relay answered.
				if lastErr == nil {
					lastErr = err
				}
				break
			}
			if attempt > 1 && opts.OnRetry != nil {
				opts.OnRetry(r, attempt, lastErr)
			}
			rctx, cancel := context.WithTimeout(ctx, opts.perAttempt())
			evs, err := nostr.FetchMany(rctx, r, filter)
			cancel()
			if err != nil {
				lastErr = err
				continue
			}
			for _, e := range evs {
				if e != nil {
					seen[e.ID] = e
				}
			}
			ok = true
			break
		}
		if ok {
			out.Answered = append(out.Answered, r)
		} else {
			out.Failed = append(out.Failed, relayFailure{Relay: r, Err: lastErr})
		}
	}

	out.Events = make([]*nostr.Event, 0, len(seen))
	for _, e := range seen {
		out.Events = append(out.Events, e)
	}
	return out
}
