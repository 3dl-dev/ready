package main

// Re-addressing recovery for CEK grants stranded by the shared-slot era
// (ready-12c, root cause ready-889).
//
// Before per-epoch slots, every epoch's grant for a grantee shared the addressable
// coordinate "<boardD>:<grantee>", so a rotation's new-epoch grant REPLACED the
// previous one and a NIP-01 relay stopped serving the old epoch's wrapped CEK. The
// key survived only in whatever local append-only logs happened to still hold it.
// This command puts those grants back, by re-emitting each one at the per-epoch
// coordinate it should have occupied all along.

import (
	"context"
	"fmt"
	"sort"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/3dl-dev/ready/pkg/nostr"
	rdSync "github.com/3dl-dev/ready/pkg/sync"
)

// strandedGrant is one owner-signed CEK-bearing grant that predates per-epoch
// slots: it sits in the shared slot, so a newer epoch's grant has replaced it on
// every relay.
type strandedGrant struct {
	Src       *nostr.Event // the original, still in the local log
	Grantee   string
	Role      string
	Epoch     int
	OldD      string
	NewD      string
	CreatedAt int64
}

// planEpochRepublish finds every owner-signed CEK-bearing grant in the log that is
// still addressed at the SHARED slot, and returns one entry per (grantee, epoch) —
// the newest, when a grantee received the same epoch more than once.
//
// It reads only what is already signed. It never unwraps, re-wraps or mints key
// material: the recovery is a re-ADDRESSING, so the bytes a member receives are the
// bytes the owner originally sealed to them. That also means a grantee whose wrap
// this machine cannot open (every grantee but the owner) is recovered just as
// faithfully as one it can.
func planEpochRepublish(events []*nostr.Event, boardAuthor, boardD string, onlyEpoch int) []strandedGrant {
	coord := rdSync.BoardCoord(boardAuthor, boardD)
	// Slots that already hold an owner-signed CEK grant. A stranded grant whose
	// per-epoch slot is already occupied has been recovered — the shared-slot
	// original stays in the append-only log forever, so without this the command
	// would re-emit it on every run and never converge.
	occupied := map[string]bool{}
	for _, e := range events {
		if e == nil || e.Kind != rdSync.KindRoleGrant || e.PubKey != boardAuthor {
			continue
		}
		if tagVal1(e, "cek") != "" && tagVal1(e, "a") == coord {
			occupied[tagVal1(e, "d")] = true
		}
	}
	newest := map[string]strandedGrant{}
	for _, e := range events {
		if e == nil || e.Kind != rdSync.KindRoleGrant || e.PubKey != boardAuthor {
			continue
		}
		if tagVal1(e, "a") != coord {
			continue
		}
		wrapped := tagVal1(e, "cek")
		if wrapped == "" {
			continue // no key material: the bare authz slot is correct for it
		}
		epoch, err := strconv.Atoi(tagVal1(e, "cek_epoch"))
		if err != nil || epoch < 1 {
			continue
		}
		if onlyEpoch > 0 && epoch != onlyEpoch {
			continue
		}
		grantee := tagVal1(e, "p")
		if grantee == "" {
			continue
		}
		oldD := tagVal1(e, "d")
		newD := boardD + ":" + grantee + ":e" + strconv.Itoa(epoch)
		if oldD == newD || occupied[newD] {
			continue // already in, or already recovered into, its per-epoch slot
		}
		key := grantee + "@" + strconv.Itoa(epoch)
		cur, seen := newest[key]
		if seen && !(e.CreatedAt > cur.CreatedAt || (e.CreatedAt == cur.CreatedAt && e.ID > cur.Src.ID)) {
			continue
		}
		newest[key] = strandedGrant{
			Src: e, Grantee: grantee, Role: tagVal1(e, "role"),
			Epoch: epoch, OldD: oldD, NewD: newD, CreatedAt: e.CreatedAt,
		}
	}
	out := make([]strandedGrant, 0, len(newest))
	for _, g := range newest {
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Epoch != out[j].Epoch {
			return out[i].Epoch < out[j].Epoch
		}
		return out[i].Grantee < out[j].Grantee
	})
	return out
}

// buildRepublishedGrant re-emits g at its per-epoch coordinate.
//
// createdAt is PRESERVED from the original, deliberately. A grant's authz weight is
// latest-wins per grantee by (created_at, id) across every slot, so stamping the
// copy "now" would float an old grant to the top of that ordering and could
// resurrect authority a later revoke had removed. Keeping the original timestamp
// makes the copy sort exactly where the original does, which is the whole intent:
// this changes WHERE the grant is stored, never WHAT it says or when it said it.
func buildRepublishedGrant(k *nostr.Key, g strandedGrant, boardAuthor, boardD string) (*nostr.Event, error) {
	from := int64(0)
	if v := tagVal1(g.Src, "from"); v != "" {
		parsed, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("grant %s: unparseable from tag %q: %w", g.Src.ID[:8], v, err)
		}
		from = parsed
	}
	spec := rdSync.RoleGrantSpec{
		BoardD:      boardD,
		BoardAuthor: boardAuthor,
		Grantee:     g.Grantee,
		Role:        g.Role,
		From:        from,
		Label:       g.Src.Content,
		Claim:       tagVal1(g.Src, "claim"),
		WrappedCEK:  tagVal1(g.Src, "cek"),
		CEKEpoch:    g.Epoch,
		WrappedLTK:  tagVal1(g.Src, "ltk"),
	}
	return rdSync.BuildRoleGrantEvent(k, spec, g.CreatedAt)
}

// tagVal1 returns the first value of the first tag named name.
func tagVal1(e *nostr.Event, name string) string {
	for _, tg := range e.Tags {
		if len(tg) >= 2 && tg[0] == name {
			return tg[1]
		}
	}
	return ""
}

var confidentialRepublishEpochCmd = &cobra.Command{
	Use:   "republish-epochs",
	Short: "Re-address old CEK grants stranded in the shared slot so relays serve them again",
	// A failed read-back is a RESULT to act on, not a misuse of the command.
	SilenceUsage: true,
	Long: `Put back the epoch grants a pre-ready-889 rotation knocked off the relays.

CEK-bearing grants used to share one addressable slot per grantee, so publishing
epoch N+1 REPLACED epoch N and relays stopped serving the older key. Every machine
that already held the board kept its keys (the local log is append-only), but a
machine seeding from relays afterwards got only the newest epoch and rendered every
pre-rotation card as [encrypted].

This re-emits each stranded grant at the per-epoch coordinate it should have had,
copying the ORIGINAL wrapped key bytes and the ORIGINAL created_at. Nothing is
unwrapped, re-wrapped or minted, and no membership decision is re-made: a grantee
receives exactly what the owner sealed to it originally, and a key that never held
an epoch does not gain one. Only the board OWNER can run it, because only the
owner's signature is honoured as a source of board keys.

Preview with --dry-run first.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		epoch, _ := cmd.Flags().GetInt("epoch")
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
		if pub.Key.PubKeyHex() != boardAuthor {
			return fmt.Errorf("only the board owner can republish grants: this board is authored by %s, you are %s", boardAuthor, pub.Key.PubKeyHex())
		}
		events, err := pub.Log.ReadAll()
		if err != nil {
			return fmt.Errorf("reading log: %w", err)
		}

		plan := planEpochRepublish(events, boardAuthor, boardD, epoch)
		coord := rdSync.BoardCoord(boardAuthor, boardD)
		fmt.Printf("board %s\n", coord)
		if len(plan) == 0 {
			fmt.Printf("  nothing stranded: every owner-signed CEK grant in this log already sits in its per-epoch slot.\n")
			return nil
		}
		fmt.Printf("  %d grant(s) to re-address:\n", len(plan))
		for _, g := range plan {
			fmt.Printf("    epoch %d  %s  role=%s\n", g.Epoch, g.Grantee, g.Role)
			fmt.Printf("      %s  ->  %s\n", g.OldD, g.NewD)
			fmt.Printf("      from event %s (created_at %d, preserved)\n", g.Src.ID[:12], g.CreatedAt)
		}
		fmt.Printf("\n  the wrapped key bytes and created_at are copied verbatim; nothing is unwrapped or re-minted.\n")

		if dryRun {
			fmt.Printf("\n--dry-run: nothing published.\n")
			return nil
		}

		built := make([]*nostr.Event, 0, len(plan))
		for _, g := range plan {
			ev, berr := buildRepublishedGrant(pub.Key, g, boardAuthor, boardD)
			if berr != nil {
				return berr
			}
			built = append(built, ev)
		}
		if _, err := pub.PublishEvents(context.Background(), built); err != nil {
			return fmt.Errorf("publishing re-addressed grants: %w", err)
		}
		fmt.Printf("\npublished %d re-addressed grant(s).\n", len(built))

		if noVerify {
			fmt.Printf("\n--no-verify: the local log is the only evidence these reached anyone.\n")
			return nil
		}
		relays := verifyRelays
		if len(relays) == 0 {
			relays = nostrReadRelays()
		}
		if len(relays) == 0 {
			fmt.Printf("\nno relays configured — nothing to read back from.\n")
			return nil
		}
		return verifyRepublishOnRelays(relays, built)
	},
}

// verifyRepublishOnRelays reads the re-addressed grants back with a FRESH
// subscription per relay and verifies each independently.
//
// rd's own publish result cannot answer this question: reduceEventOutcome
// (pkg/sync/relayclass.go) reports an event accepted the moment ANY write relay
// accepts it, and the LAN relays accept everything. Whether the PUBLIC relay a
// remote member reads from actually took these grants is exactly the fact this
// recovery exists to establish, so it is read back, not assumed.
func verifyRepublishOnRelays(relays []string, published []*nostr.Event) error {
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
			fmt.Printf("  %s  %d/%d re-addressed grant(s) present\n", r, len(rb.Present), rb.Want)
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
		return fmt.Errorf("the re-addressed grants are NOT readable on %v — the old epochs are still stranded there; retry, or `rd relay repair --relay <url>`", failed)
	}
	return nil
}

func init() {
	confidentialRepublishEpochCmd.Flags().Bool("dry-run", false, "print exactly which grants would be re-addressed; publish nothing")
	confidentialRepublishEpochCmd.Flags().Int("epoch", 0, "restrict to a single CEK epoch (default: every stranded epoch)")
	confidentialRepublishEpochCmd.Flags().StringArray("verify", nil, "relay URL to read the re-addressed grants back from (repeatable; defaults to the configured read relays)")
	confidentialRepublishEpochCmd.Flags().Bool("no-verify", false, "skip the relay read-back")
	confidentialCmd.AddCommand(confidentialRepublishEpochCmd)
}
