package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/3dl-dev/ready/pkg/identity"
	"github.com/3dl-dev/ready/pkg/nostr"
	rdSync "github.com/3dl-dev/ready/pkg/sync"
	"github.com/spf13/cobra"
)

// `rd identify` publishes a signed kind-39302 person-alias asserting that the
// current machine key belongs to a named PARTY (a human operator) whose stable
// handle is an email. It is the write side of the local party resolver
// (pkg/identity): once published, PartyForPubkey / KeysForParty can map any of the
// party's keys to the operator and back (ready-034, sharp edges #1 / #6).
//
// TRUST MODEL (v1, single operator): the alias is signed by THIS machine key, so it
// is honored by any reader whose trust closure already contains this key — i.e. the
// operator's own machines. Accepting a THIRD PARTY's alias is out of scope for v1
// (see pkg/identity package doc).

// nextAliasCreatedAt returns a strictly-monotonic created_at for THIS party's alias
// slot (kind 39302, same "d"=handle): max(now, newest-same-handle + 1). This keeps
// a re-published alias from tying (and losing the id tie-break) against the one it
// means to supersede, without touching the shared DriftScope projection helper.
func nextAliasCreatedAt(log *rdSync.NostrLog, handle string) int64 {
	now := time.Now().Unix()
	events, err := log.ReadAll()
	if err != nil {
		return now
	}
	var max int64
	for _, e := range events {
		if e == nil || e.Kind != identity.KindPersonAlias {
			continue
		}
		// same addressable slot = same handle in the "d" tag
		d := ""
		for _, t := range e.Tags {
			if len(t) >= 2 && t[0] == "d" {
				d = t[1]
				break
			}
		}
		if d != handle {
			continue
		}
		if e.CreatedAt > max {
			max = e.CreatedAt
		}
	}
	if max+1 > now {
		return max + 1
	}
	return now
}

var identifyCmd = &cobra.Command{
	Use:   "identify",
	Short: "Declare which party (person) this machine key belongs to (signed person-alias)",
	Long: `Publish a signed person-alias asserting that this machine's key — together with
any --add-key keys — belongs to ONE party (a human operator) whose stable handle is
an email. Other machines of the same operator can then resolve pubkey->party and
email->keys locally, with no central directory.

At least one --add-email is required (its first value is the party handle). This
key's own pubkey is always part of the party. Re-running supersedes the previous
alias for the same handle (addressable latest-wins).

TRUST: readers honor this alias only if this key is already in their trust closure
(the operator's own machines). Accepting a third party's alias is out of scope.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		emails, _ := cmd.Flags().GetStringArray("add-email")
		addKeys, _ := cmd.Flags().GetStringArray("add-key")
		label, _ := cmd.Flags().GetString("label")

		if len(emails) == 0 {
			return fmt.Errorf("at least one --add-email is required (the first is the party handle)")
		}

		if !nostrWriteActive() {
			return fmt.Errorf("nostr publish path is disabled; set RD_NOSTR=1 or run on a nostr-native project")
		}
		pub, ok, err := nostrPublisher()
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("no .ready project directory found")
		}

		self := pub.Key.PubKeyHex()
		// Party keys = this machine key ∪ every --add-key (validated hex).
		pubkeys := []string{self}
		for _, pk := range addKeys {
			if len(pk) != 64 || !isHex(pk) {
				return fmt.Errorf("--add-key %q is not a valid pubkey: must be a 64-character hex string", pk)
			}
			pubkeys = append(pubkeys, pk)
		}

		spec := identity.AliasSpec{
			Handle:  emails[0],
			Pubkeys: pubkeys,
			Emails:  emails,
			Label:   label,
		}
		ev, err := identity.BuildAliasEvent(pub.Key, spec, nextAliasCreatedAt(pub.Log, spec.Handle))
		if err != nil {
			return err
		}

		res, err := pub.PublishEvents(context.Background(), []*nostr.Event{ev})
		if err != nil {
			return err
		}
		anyRelay := false
		for _, a := range res.Events {
			if a.AnyRelay {
				anyRelay = true
			}
		}
		fmt.Printf("published person-alias: party=%s keys=%d emails=%d\n", spec.Handle, len(pubkeys), len(emails))
		fmt.Printf("  event id=%s relay-accepted=%v\n", ev.ID, anyRelay)
		if res.Buffered {
			fmt.Println("  (reached no relay; buffered to nostr-pending.jsonl — durable in local log)")
		}
		if res.Rejected {
			fmt.Fprintln(os.Stderr, "  WARNING: the person-alias event was permanently rejected by a relay and dead-lettered to nostr-rejected.jsonl — NOT retried; inspect and fix.")
		}
		return nil
	},
}

func init() {
	identifyCmd.Flags().StringArray("add-email", nil, "email handle for this party (repeatable; first is the party handle) — required")
	identifyCmd.Flags().StringArray("add-key", nil, "additional pubkey hex to bind to this party (repeatable)")
	identifyCmd.Flags().String("label", "", "optional human display name for the party")
	rootCmd.AddCommand(identifyCmd)
}
