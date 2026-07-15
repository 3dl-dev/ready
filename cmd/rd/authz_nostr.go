package main

import (
	"fmt"
	"os"
	"sort"
	"text/tabwriter"

	"github.com/3dl-dev/ready/pkg/nostr"
	rdSync "github.com/3dl-dev/ready/pkg/sync"
	"github.com/spf13/cobra"
)

// ============================================================================
// ready-477 — the kind-39301 role-grant is the SOLE authz + audit path on the
// nostr-native default path.
//
// `rd grant` / `rd revoke` / `rd kill` publish an OWNER-signed kind-39301
// role-grant (pkg/sync/rolegrant.go) — never a cf-authority delegation grant —
// and then REGENERATE the relay write-allowlist from the signed log, surfacing the
// diff in the command output (design docs/design/nostr-identity-model.md §3-§4).
// `rd sessions` lists the active grant-holders derived from that same signed log.
//
// The cf-authority delegation path (cmd/rd/delegation_grant.go, the campfire
// branches of sessions.go / kill.go / revoke.go) stays PRESENT for campfire-backed
// projects but is NEVER invoked on the nostr-native default path (I7/ready-cb6
// deletes the vestige). Each command routes to the nostr body when
// nostrNativeProject() is true, before any requireClient()/protocol.Init, so the
// default path provisions no .cf.
// ============================================================================

// ownerBootstrapLabel labels the board author (owner) key in the regenerated relay
// write-allowlist — the bootstrap trust root has no grant of its own.
const ownerBootstrapLabel = "board author (owner) — bootstrap trust root"

// runNostrGrantRevoke is the shared body of `rd grant`/`rd revoke`/`rd kill` on the
// nostr-native path: it publishes the owner-signed kind-39301 grant (the durable
// authoritative act) and then regenerates the relay write-allowlist from the signed
// log, surfacing the add/remove/preserve diff. A regeneration failure WARNS but never
// fails the command — the grant is already durable in the log; the allowlist is a
// derived artifact the operator can re-emit with `rd relay sync-allowlist`.
func runNostrGrantRevoke(dir, grantee, role, label string, from int64, claim string) error {
	if err := publishRoleGrant(grantee, role, label, from, claim); err != nil {
		return err
	}
	// Confidential-by-default (ready-216): revoking read access on a confidential
	// board rotates the CEK epoch — mint a new epoch and re-wrap it to the remaining
	// members, so cards authored after the revoke are unreadable by the revoked key
	// (forward secrecy). No-op on a plaintext board or a non-owner signer.
	if role == rdSync.RoleRevoked {
		if pub, ok, perr := nostrPublisher(); perr == nil && ok {
			if boardAuthor, boardD, berr := resolveBoardAuthorD(dir, pub.Key.PubKeyHex()); berr == nil {
				if rerr := rekeyBoardOnRevoke(dir, pub, boardAuthor, boardD, grantee); rerr != nil {
					fmt.Fprintf(os.Stderr, "warning: confidential rekey after revoke failed: %v\n", rerr)
				}
			}
		}
	}
	surfaceAllowlistRegen(dir)
	return nil
}

// surfaceAllowlistRegen regenerates the local relay write-allowlist from the signed
// grants and prints the diff. Best-effort (see runNostrGrantRevoke).
func surfaceAllowlistRegen(dir string) {
	file := defaultAllowlistFile()
	baseline, err := readAllowlistFile(file)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not read current relay write-allowlist %s: %v\n", file, err)
		return
	}
	plan, err := regenerateAllowlistLocal(dir, file, baseline)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: relay write-allowlist NOT regenerated: %v\n", err)
		fmt.Fprintln(os.Stderr, "  run 'rd relay sync-allowlist' once resolved")
		return
	}
	fmt.Printf("regenerated relay write-allowlist %s (%d key(s) admitted)\n", file, len(plan.Final))
	printKeyList("ADD", plan.Added, plan.Final)
	printKeyList("REMOVE", plan.Removed, baseline)
	printKeyList("PRESERVE (admitted, no rd grant — kept)", plan.Preserved, baseline)
	fmt.Println("  next: 'rd relay sync-allowlist --apply' to push the update to the relays")
}

// regenerateAllowlistLocal derives the relay write-allowlist from the signed grants
// for this project's pinned board, reconciles it against baseline under the
// no-lockout invariant (PlanAllowlist), writes the resulting file, and returns the
// plan. It writes NOTHING and errors when the no-lockout guard trips, so a bad
// derivation can never lock a currently-admitted writer out of the relays. Pure of
// relay I/O — the caller supplies the baseline and file path — so it is unit-testable
// without a relay.
func regenerateAllowlistLocal(dir, file string, baseline map[string]string) (rdSync.AllowlistPlan, error) {
	var zero rdSync.AllowlistPlan
	k, err := nostrKey()
	if err != nil {
		return zero, err
	}
	boardAuthor, boardD, err := resolveBoardAuthorD(dir, k.PubKeyHex())
	if err != nil {
		return zero, err
	}
	events, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
	if err != nil {
		return zero, err
	}
	plan := rdSync.PlanAllowlist(events, boardAuthor, boardD, ownerBootstrapLabel, baseline)
	if len(plan.LockoutViolations) > 0 {
		return plan, fmt.Errorf("no-lockout guard tripped: %d key(s) would be locked out without a revoke grant", len(plan.LockoutViolations))
	}
	if err := writeAllowlistFile(file, plan.Final); err != nil {
		return plan, err
	}
	return plan, nil
}

// nostrHolder is one active authz principal shown by `rd sessions` on the
// nostr-native path: the owner (root principal) or a non-revoked grantee.
type nostrHolder struct {
	Pubkey string `json:"pubkey"`
	Role   string `json:"role"`
	Label  string `json:"label,omitempty"`
}

// nostrSessionHolders derives the active grant-holders from the signed kind-39301
// role-grants for the board coordinate 30301:<owner>:<boardD>: the owner (root
// principal, always present) plus every grantee whose winning cap-valid grant is
// non-revoked, each labelled with its role. Revoked keys (level 0) are EXCLUDED — a
// revoked key holds no active session. Pure (no I/O) for unit-testing.
func nostrSessionHolders(events []*nostr.Event, owner, boardD string) []nostrHolder {
	levels, _ := rdSync.DeriveLevels(events, owner, boardD)
	labels := rdSync.DeriveAllowlist(events, owner, boardD, ownerBootstrapLabel)
	holders := make([]nostrHolder, 0, len(levels))
	for pk, lvl := range levels {
		if lvl == rdSync.LevelRevoked {
			continue // revoked — no active session.
		}
		holders = append(holders, nostrHolder{Pubkey: pk, Role: roleForLevel(pk, owner, lvl), Label: labels[pk]})
	}
	sort.Slice(holders, func(i, j int) bool { return holders[i].Pubkey < holders[j].Pubkey })
	return holders
}

// roleForLevel names an actor's role for display: the board author is the owner
// (root principal); a level-2 key is a maintainer; anything else is a contributor.
// owner and maintainer share numeric level 2 — the board-author identity, not the
// level number, distinguishes them (design §3).
func roleForLevel(pubkey, owner string, level int) string {
	if pubkey == owner {
		return "owner"
	}
	if level >= rdSync.LevelMaintainer {
		return "maintainer"
	}
	return "contributor"
}

// runSessionsNostr lists the active grant-holders derived from the signed
// kind-39301 log for the pinned board — the nostr-native `rd sessions` body. It
// performs NO campfire init and provisions NO .cf.
func runSessionsNostr(dir string, jsonOut bool) error {
	owner, boardD, ok := rdSync.ParseBoardCoord(nostrPinnedBoard(dir))
	if !ok {
		return fmt.Errorf("no pinned board coordinate in .ready/config.json; pin one with 'rd pin-board'")
	}
	events, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
	if err != nil {
		return fmt.Errorf("reading nostr log: %w", err)
	}
	holders := nostrSessionHolders(events, owner, boardD)

	if jsonOut {
		return emitMutationResult("", map[string]any{"holders": holders})
	}
	if len(holders) == 0 {
		fmt.Fprintln(os.Stdout, "no active grant-holders")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "PUBKEY\tROLE\tLABEL")
	for _, h := range holders {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", shortKey(h.Pubkey), h.Role, h.Label)
	}
	return tw.Flush()
}

// grantCmd is the top-level `rd grant` — the promoted, sole authz grant op. On a
// nostr-native project it publishes an owner-signed kind-39301 role-grant and
// regenerates the relay write-allowlist. It is nostr-native only: campfire-backed
// projects admit members with `rd admit`, not `rd grant`.
var grantCmd = &cobra.Command{
	Use:   "grant <pubkeyHex> <role>",
	Short: "Grant a role (owner|maintainer|contributor) via an owner-signed kind-39301 role-grant",
	Long: `Publish an owner-signed kind-39301 role-grant assigning <role> to <pubkeyHex>
for this project's pinned board, then regenerate the relay write-allowlist from the
signed grants so the grantee is admitted in ONE act. role is one of
owner|maintainer|contributor. Only the board author (owner) may grant maintainer or
owner (the escalation cap).

This is the nostr-native authz path: it provisions no legacy .cf. Revoke with
'rd revoke <pubkeyHex>'.`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		grantee, role := args[0], args[1]
		switch role {
		case rdSync.RoleOwner, rdSync.RoleMaintainer, rdSync.RoleContributor:
		case rdSync.RoleRevoked:
			return fmt.Errorf("use 'rd revoke %s' to revoke; 'grant' is for owner|maintainer|contributor", grantee)
		default:
			return fmt.Errorf("invalid role %q: choose owner|maintainer|contributor", role)
		}
		dir, native := nostrNativeProject()
		if !native {
			return fmt.Errorf("rd grant operates on a nostr-native project (kind-39301 role-grants); pin a board with 'rd pin-board' first")
		}
		label, _ := cmd.Flags().GetString("label")
		from, _ := cmd.Flags().GetInt64("from")
		claim, _ := cmd.Flags().GetString("claim")
		return runNostrGrantRevoke(dir, grantee, role, label, from, claim)
	},
}

func init() {
	grantCmd.Flags().String("label", "", "human label carried in the grant content (used as the relay allowlist label)")
	grantCmd.Flags().Int64("from", 0, "effective-from unix seconds (0 = prospective / effective now)")
	grantCmd.Flags().String("claim", "", "one-use invite claim-nonce this grant consumes (from the joiner's `rd join` output); binds the nonce to this pubkey (single-use)")
	rootCmd.AddCommand(grantCmd)
}
