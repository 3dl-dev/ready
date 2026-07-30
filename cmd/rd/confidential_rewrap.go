package main

// `rd confidential rewrap` — re-mint an existing epoch's wrapped keys in the
// browser-safe HEX payload encoding, without touching the keys themselves
// (ready-470, root cause ready-c4b).
//
// THE BUG THIS ANSWERS. A browser can only unwrap a CEK through NIP-07's
// `window.nostr.nip44.decrypt`, whose return type is a STRING: every extension
// finishes with a UTF-8 TextDecoder, and 32 random bytes are essentially never
// valid UTF-8, so a non-fatal decoder substitutes U+FFFD and destroys the key
// with no error raised anywhere. pkg/sync/keydist.go's WrapKey therefore seals 64
// lowercase-hex characters, which survive that boundary byte-for-byte. Grants
// MINTED BEFORE that change carry raw-byte payloads: the Go CLI still reads them
// (unwrapKey accepts both encodings) but the board page cannot, so every card on
// such a board renders as [encrypted] — including for the OWNER.
//
// WHY NOT JUST ROTATE. `rd confidential rotate` already re-wraps to every member,
// but it mints a NEW epoch, and cards sealed under the old one then need the old
// key too — a reader that only ever receives the new epoch reads none of the
// board's history. Nothing about the CEK is wrong here; only the encoding of its
// wrap is. So this command re-seals THE SAME KEY VALUE, at THE SAME EPOCH, into
// THE SAME addressable slot, and re-seals nothing else: not one card is touched.
//
// WHAT IT IS NOT ALLOWED TO DO, and what enforces that:
//   - change any key value: the value is read back OUT of the wrap being replaced
//     (the owner can open its own wraps — NIP-44's conversation key is symmetric)
//     and cross-checked against the owner's keyring for that epoch.
//   - hand a key to someone who does not already hold one: the plan is built from
//     the grants that EXIST, never from a membership list, so it can only re-mint
//     a wrap that was already issued.
//   - hand a key back to a revoked member: a grantee whose winning grant is
//     `revoked` is withheld and reported.

import (
	"context"
	"fmt"
	"sort"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/3dl-dev/ready/pkg/nostr"
	rdSync "github.com/3dl-dev/ready/pkg/sync"
)

// rewrapTarget is one owner-signed CEK-bearing grant slot, with the key material
// it carries already recovered from the wrap that is about to be replaced.
type rewrapTarget struct {
	Src     *nostr.Event
	Grantee string
	Role    string
	Epoch   int
	// CEK/LTK are the values recovered from Src's own wraps — what gets re-sealed.
	CEK    [32]byte
	LTK    [32]byte
	HasLTK bool
	// BrowserSafe is true when every wrap this grant carries ALREADY uses the hex
	// payload, i.e. there is nothing to do for it. Those entries are reported and
	// skipped, which is what makes a second run a no-op.
	BrowserSafe bool
	// CreatedAt is the timestamp the replacement will carry: one second after the
	// newest event this (grantee, epoch) already has. See buildRewrappedGrant.
	CreatedAt int64
}

// rewrapPlan is what `rd confidential rewrap` WOULD publish, computed from the
// local log with nothing written. --dry-run prints it; the command executes the
// same struct, so the preview and the act cannot disagree.
type rewrapPlan struct {
	Coord string
	// Targets are the grants to re-mint, oldest epoch first then by grantee.
	Targets []rewrapTarget
	// AlreadyHex are the grants a browser can read as they stand.
	AlreadyHex []rewrapTarget
	// Withheld are grantees holding a CEK grant whose winning role is `revoked`:
	// they keep the raw wrap they were given (their Go CLI still reads it) and are
	// deliberately not upgraded to browser-readable.
	Withheld []string
}

// planBoardRewrap builds the plan from the local log alone — no relay, no writes.
//
// It considers EVERY epoch the board has, not only the current one: each epoch
// sits in its own addressable slot (rolegrant.go roleGrantD) and each is needed to
// read the cards sealed under it, so leaving an older epoch raw would leave the
// browser showing [encrypted] for exactly that stretch of the board's history.
// Re-minting an old epoch's wrap is the same operation as re-minting the current
// one — same key, same slot — and no more privileged.
func planBoardRewrap(dir string, pub *rdSync.Publisher, boardAuthor, boardD string) (*rewrapPlan, error) {
	coord := rdSync.BoardCoord(boardAuthor, boardD)
	if !boardIsConfidential(dir) {
		return nil, fmt.Errorf("board %s is PUBLIC — it has no wrapped keys to re-mint", coord)
	}
	signer := pub.Key.PubKeyHex()
	if signer != boardAuthor {
		return nil, fmt.Errorf("only the board OWNER can re-wrap board keys: board %s is owned by %s, this key is %s", coord, boardAuthor, signer)
	}
	events, err := pub.Log.ReadAll()
	if err != nil {
		return nil, err
	}
	kr := rdSync.DeriveBoardKeyring(events, pub.Key, boardAuthor, boardD)
	holders := rdSync.DeriveGrantHolders(events, boardAuthor, boardD)

	// Newest owner-signed CEK-bearing grant per (grantee, epoch), plus the newest
	// created_at seen anywhere in that group — the replacement has to sort after
	// every one of them for a relay to serve it instead.
	type groupKey struct {
		grantee string
		epoch   int
	}
	newest := map[groupKey]*nostr.Event{}
	latest := map[groupKey]int64{}
	for _, e := range events {
		if e == nil || e.Kind != rdSync.KindRoleGrant || e.PubKey != boardAuthor {
			continue
		}
		if tagVal1(e, "a") != coord || tagVal1(e, "cek") == "" {
			continue
		}
		epoch, cerr := strconv.Atoi(tagVal1(e, "cek_epoch"))
		if cerr != nil || epoch < 1 {
			continue
		}
		grantee := tagVal1(e, "p")
		if grantee == "" {
			continue
		}
		if err := e.Verify(); err != nil {
			continue
		}
		k := groupKey{grantee, epoch}
		if e.CreatedAt > latest[k] {
			latest[k] = e.CreatedAt
		}
		cur, seen := newest[k]
		if seen && !(e.CreatedAt > cur.CreatedAt || (e.CreatedAt == cur.CreatedAt && e.ID > cur.ID)) {
			continue
		}
		newest[k] = e
	}

	plan := &rewrapPlan{Coord: coord}
	withheld := map[string]bool{}
	for k, src := range newest {
		if h, ok := holders[k.grantee]; ok && h.Level == rdSync.LevelRevoked {
			withheld[k.grantee] = true
			continue
		}
		t, terr := inspectRewrapTarget(pub.Key, kr, coord, src, k.grantee, k.epoch, latest[k])
		if terr != nil {
			return nil, terr
		}
		if t.BrowserSafe {
			plan.AlreadyHex = append(plan.AlreadyHex, t)
			continue
		}
		plan.Targets = append(plan.Targets, t)
	}
	sortTargets(plan.Targets)
	sortTargets(plan.AlreadyHex)
	for pk := range withheld {
		plan.Withheld = append(plan.Withheld, pk)
	}
	sort.Strings(plan.Withheld)
	return plan, nil
}

// inspectRewrapTarget opens the wraps src carries and reports what they hold and
// how they are encoded.
//
// The owner can open a wrap it sealed to a MEMBER: NIP-44's conversation key is
// ECDH(owner_sec, member_pub), which is the same key on both sides. That is what
// lets this command recover the exact value to re-seal instead of substituting a
// key from elsewhere — the re-wrap carries the bytes the member already has.
//
// A recovered value is still cross-checked against the owner's own keyring for
// that epoch when the owner holds it. A mismatch means two different keys are in
// circulation for one epoch, which re-wrapping would silently pick a winner for;
// refuse and say so instead.
func inspectRewrapTarget(owner *nostr.Key, kr *rdSync.BoardKeyring, coord string, src *nostr.Event, grantee string, epoch int, latest int64) (rewrapTarget, error) {
	t := rewrapTarget{Src: src, Grantee: grantee, Role: tagVal1(src, "role"), Epoch: epoch, CreatedAt: latest + 1}
	cek, cekHex, err := rdSync.UnwrapKeyPayload(owner, grantee, tagVal1(src, "cek"))
	if err != nil {
		return t, fmt.Errorf("grant %s (epoch %d, grantee %s): its wrapped CEK does not open for the owner key, so its value cannot be re-sealed: %w", src.ID[:12], epoch, shortKey(grantee), err)
	}
	if held, ok := kr.CEK(coord, epoch); ok && held != cek {
		return t, fmt.Errorf("grant %s (epoch %d, grantee %s) carries a CEK that is NOT the one the owner holds for that epoch — two keys are in circulation for one epoch; refusing to re-wrap, because doing so would pick a winner silently", src.ID[:12], epoch, shortKey(grantee))
	}
	t.CEK, t.BrowserSafe = cek, cekHex

	wLTK := tagVal1(src, "ltk")
	if wLTK == "" {
		return t, nil
	}
	ltk, ltkHex, err := rdSync.UnwrapKeyPayload(owner, grantee, wLTK)
	if err != nil {
		return t, fmt.Errorf("grant %s (epoch %d, grantee %s): its wrapped LTK does not open for the owner key: %w", src.ID[:12], epoch, shortKey(grantee), err)
	}
	if held, ok := kr.LTK(coord); ok && held != ltk {
		return t, fmt.Errorf("grant %s (epoch %d, grantee %s) carries an LTK that is NOT the one the owner holds — refusing to re-wrap", src.ID[:12], epoch, shortKey(grantee))
	}
	t.LTK, t.HasLTK = ltk, true
	t.BrowserSafe = t.BrowserSafe && ltkHex
	return t, nil
}

// sortTargets orders by epoch then grantee, so a run is diffable against the last.
func sortTargets(ts []rewrapTarget) {
	sort.Slice(ts, func(i, j int) bool {
		if ts[i].Epoch != ts[j].Epoch {
			return ts[i].Epoch < ts[j].Epoch
		}
		return ts[i].Grantee < ts[j].Grantee
	})
}

// buildRewrappedGrant re-emits t's grant with hex-encoded wraps of the SAME keys.
//
// EVERYTHING ELSE IS COPIED FROM THE ORIGINAL — role, label, from, claim, epoch —
// so the replacement says exactly what the original said. Re-issuing at the
// member's CURRENT role would be wrong here: this is not a new authorization
// decision, and a grant that has since been superseded must not be re-stated in
// today's terms.
//
// CREATED_AT IS THE ORIGINAL'S, PLUS ONE SECOND, AND DELIBERATELY NOT "NOW". A
// relay keeps only the newest event per (kind, pubkey, d), so the replacement must
// be at least a second newer than what it replaces or the relay keeps serving the
// raw wrap. It must ALSO stay next to the original, because a board's confidential
// CUTOVER is the earliest created_at of any owner-signed CEK-bearing grant
// (keydist.go DeriveBoardKeyring): stamping these "now" would move the cutover of
// a relay-seeded reader forward by however long the board has existed, and every
// plaintext card written in between — which the fold gate exists to quarantine —
// would start reading as grandfathered. One second is the smallest step that
// replaces the event, and it moves the grandfather boundary by one second.
func buildRewrappedGrant(k *nostr.Key, t rewrapTarget, boardAuthor, boardD string) (*nostr.Event, error) {
	from := int64(0)
	if v := tagVal1(t.Src, "from"); v != "" {
		parsed, perr := strconv.ParseInt(v, 10, 64)
		if perr != nil {
			return nil, fmt.Errorf("grant %s: unparseable from tag %q: %w", t.Src.ID[:8], v, perr)
		}
		from = parsed
	}
	wCEK, err := rdSync.WrapKey(k, t.Grantee, t.CEK)
	if err != nil {
		return nil, err
	}
	spec := rdSync.RoleGrantSpec{
		BoardD:      boardD,
		BoardAuthor: boardAuthor,
		Grantee:     t.Grantee,
		Role:        t.Role,
		From:        from,
		Label:       t.Src.Content,
		Claim:       tagVal1(t.Src, "claim"),
		WrappedCEK:  wCEK,
		CEKEpoch:    t.Epoch,
	}
	if t.HasLTK {
		wLTK, lerr := rdSync.WrapKey(k, t.Grantee, t.LTK)
		if lerr != nil {
			return nil, lerr
		}
		spec.WrappedLTK = wLTK
	}
	return rdSync.BuildRoleGrantEvent(k, spec, t.CreatedAt)
}

var confidentialRewrapCmd = &cobra.Command{
	Use:   "rewrap",
	Short: "Re-mint this board's wrapped keys in the encoding a browser can read (same keys, same epochs)",
	// A failed read-back is a RESULT the operator must act on, not a misuse.
	SilenceUsage: true,
	Long: `Make this board's existing read keys reachable from the board page.

A browser unwraps a board key through the NIP-07 extension's nip44.decrypt, which
returns a STRING. Grants minted before that constraint was understood sealed the
key as 32 raw bytes; those bytes do not survive an extension's UTF-8 decode, so a
browser silently gets a corrupted key and every card renders as [encrypted] — for
members and for the OWNER alike. rd itself is unaffected: it reads both encodings.

This re-seals the SAME key, at the SAME epoch, into the SAME addressable slot,
with the key encoded as hex. It does NOT mint a new epoch (which would leave every
existing card unreadable to anyone who does not also hold the old key), it does NOT
re-seal, re-sign or invalidate a single card, and it does not change who holds
what: the plan is built from the grants that already exist, so it can only re-mint
a wrap that was already issued. A grantee whose grant has been revoked is withheld.

Re-running it is a no-op — a grant already carrying a hex wrap is reported and
skipped. Preview with --dry-run.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		verifyRelays, _ := cmd.Flags().GetStringArray("verify")
		noVerify, _ := cmd.Flags().GetBool("no-verify")

		dir, ok := readyProjectDir()
		if !ok {
			return errNotNostrProject()
		}
		pub, ok, err := nostrPublisher()
		if err != nil {
			return err
		}
		if !ok {
			return errNotNostrProject()
		}
		boardAuthor, boardD, err := resolveBoardAuthorD(dir, pub.Key.PubKeyHex())
		if err != nil {
			return err
		}
		plan, err := planBoardRewrap(dir, pub, boardAuthor, boardD)
		if err != nil {
			return err
		}

		fmt.Printf("board %s\n", plan.Coord)
		for _, t := range plan.AlreadyHex {
			fmt.Printf("  epoch %d  %s  already browser-readable — skipped\n", t.Epoch, shortKey(t.Grantee))
		}
		for _, pk := range plan.Withheld {
			fmt.Printf("  %s  REVOKED — withheld\n", shortKey(pk))
		}
		if len(plan.Targets) == 0 {
			fmt.Printf("  nothing to re-wrap: every live grant on this board already carries a hex-encoded key.\n")
			return nil
		}
		fmt.Printf("  %d grant(s) to re-wrap (same key value, same epoch, same slot):\n", len(plan.Targets))
		for _, t := range plan.Targets {
			fmt.Printf("    epoch %d  %s  role=%s  ltk=%t\n", t.Epoch, shortKey(t.Grantee), t.Role, t.HasLTK)
			fmt.Printf("      replaces %s (created_at %d -> %d)\n", t.Src.ID[:12], t.Src.CreatedAt, t.CreatedAt)
		}
		fmt.Printf("\n  no epoch is minted, no card is re-sealed, and no key value changes.\n")
		if dryRun {
			fmt.Printf("\n--dry-run: nothing published.\n")
			return nil
		}

		built := make([]*nostr.Event, 0, len(plan.Targets))
		for _, t := range plan.Targets {
			ev, berr := buildRewrappedGrant(pub.Key, t, boardAuthor, boardD)
			if berr != nil {
				return berr
			}
			built = append(built, ev)
		}
		if _, err := pub.PublishEvents(context.Background(), built); err != nil {
			return fmt.Errorf("publishing re-wrapped grants: %w", err)
		}
		fmt.Printf("\npublished %d re-wrapped grant(s).\n", len(built))

		if noVerify {
			fmt.Printf("\n--no-verify: the local log is the only evidence these reached anyone.\n")
			return nil
		}
		relays := verifyRelays
		if len(relays) == 0 {
			relays = nostrReadRelays()
		}
		if len(relays) == 0 {
			fmt.Printf("\nno relays configured — the re-wrapped grants exist only in the local log, so the board page still cannot read this board.\n")
			return nil
		}
		return verifyRewrapOnRelays(relays, built)
	},
}

// verifyRewrapOnRelays reads the re-wrapped grants back with a FRESH subscription
// per relay and verifies each independently.
//
// rd's own publish result cannot answer the question this command exists to
// settle. reduceEventOutcome (pkg/sync/relayclass.go) reports an event accepted
// the moment ANY write relay accepts it, and the LAN relays accept everything —
// while the only relay that matters here is the PUBLIC one the board page reads
// from. If it did not take these, the page keeps showing [encrypted] and every
// local signal says the re-wrap succeeded.
func verifyRewrapOnRelays(relays []string, published []*nostr.Event) error {
	fmt.Printf("\nread-back (fresh REQ per relay, each event schnorr-verified independently):\n")
	var failed []string
	for _, r := range relays {
		ctx, cancel := context.WithTimeout(context.Background(), rdSync.DefaultAuditTimeout)
		rb, err := rdSync.VerifyEventsOnRelay(ctx, r, published)
		cancel()
		if err != nil {
			fmt.Printf("  %s  READ FAILED: %v\n", r, err)
			failed = append(failed, r)
			continue
		}
		if rb.Match {
			fmt.Printf("  %s  %d/%d re-wrapped grant(s) present\n", r, len(rb.Present), rb.Want)
			continue
		}
		fmt.Printf("  %s  %d/%d present — MISSING %d", r, len(rb.Present), rb.Want, len(rb.Missing))
		if len(rb.Unverified) > 0 {
			fmt.Printf(" (%d served but FAILED VERIFICATION)", len(rb.Unverified))
		}
		fmt.Println()
		for _, id := range rb.Missing {
			fmt.Printf("      missing %s\n", id)
		}
		failed = append(failed, r)
	}
	if len(failed) > 0 {
		return fmt.Errorf("the re-wrapped grants are NOT readable on %v — the board page still receives the old raw-payload wraps there and will keep rendering [encrypted]; retry, or `rd relay repair --relay <url>`", failed)
	}
	return nil
}

func init() {
	confidentialRewrapCmd.Flags().Bool("dry-run", false, "print exactly which grants would be re-wrapped; publish nothing")
	confidentialRewrapCmd.Flags().StringArray("verify", nil, "relay URL to read the re-wrapped grants back from (repeatable; defaults to the configured read relays)")
	confidentialRewrapCmd.Flags().Bool("no-verify", false, "skip the relay read-back")
	confidentialCmd.AddCommand(confidentialRewrapCmd)
}
