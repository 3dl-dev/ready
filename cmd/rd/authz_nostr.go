package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
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
//
// ready-0df: this is a SIDE EFFECT of publishing a grant — it fires on every `rd
// grant`/`rd revoke`/`rd kill` and, via --all-boards, in EVERY scanned sibling repo.
// The grant EVENT (the durable, authoritative kind-39301 act) is separable from the
// relay ALLOWLIST FILE regen (an ops step `rd relay sync-allowlist --apply` owns): if
// the target file has UNCOMMITTED git changes — the operator mid-edit, e.g. a
// hand-relabeled key — silently overwriting it destroys that work with no warning or
// diff. So when the file is dirty this SKIPS the regen and warns instead of writing;
// a clean (or absent) file regenerates exactly as before. The explicit `rd relay
// sync-allowlist [--apply]` path is UNAFFECTED by this guard — it is a deliberate
// operator act, not an automatic side effect, so it may overwrite a dirty file.
//
// ready-a76: cleanliness cannot always be DETERMINED — git binary absent, or the file
// living outside any git work tree — and the original guard fail-opened that case to
// "not dirty", silently overwriting an existing hand-edit it just couldn't see. Now an
// undeterminable status is treated the same as dirty WHEN the file exists with
// non-empty content (skip + warn); an undeterminable status on an absent/empty file
// still regenerates, since there is nothing to lose there.
func surfaceAllowlistRegen(dir string) {
	file := defaultAllowlistFile()
	switch checkAllowlistGitStatus(file) {
	case allowlistStatusDirty:
		fmt.Fprintf(os.Stderr, "warning: %s has uncommitted changes; skipped regen — run 'rd relay sync-allowlist' to refresh\n", file)
		return
	case allowlistStatusUnknown:
		// ready-a76: cleanliness could not be determined at all (git binary
		// absent, or file outside any git work tree) — do NOT fail-open to "not
		// dirty" when there is something an overwrite could destroy. An existing
		// non-empty file might be an operator's uncommitted hand-edit we simply
		// can't see; skip + warn instead of silently clobbering it. An absent or
		// empty file has nothing to lose, so it still falls through and
		// regenerates below.
		if info, statErr := os.Stat(file); statErr == nil && info.Size() > 0 {
			fmt.Fprintf(os.Stderr, "warning: cannot determine git status of %s; skipped regen to avoid clobbering — run 'rd relay sync-allowlist' to refresh\n", file)
			return
		}
	}
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

// allowlistGitStatus is the tri-state result of checking whether the allowlist file
// has uncommitted git changes: clean, dirty, or genuinely undeterminable.
type allowlistGitStatus int

const (
	allowlistStatusClean allowlistGitStatus = iota
	allowlistStatusDirty
	// allowlistStatusUnknown means the git check itself could not run — git binary
	// absent, or file is not inside a git work tree — so cleanliness is unknown,
	// NOT "clean" (ready-a76: this used to fail-open to clean, silently enabling
	// an overwrite of a file whose git state we simply couldn't see).
	allowlistStatusUnknown
)

// checkAllowlistGitStatus reports whether file has UNCOMMITTED git changes (staged,
// unstaged, or untracked) — the ready-0df guard that stops the automatic grant-time
// regen from clobbering an operator's in-progress edit. When the check itself cannot
// run (git unavailable, or file outside any git work tree) it reports
// allowlistStatusUnknown rather than silently claiming clean — the caller (
// surfaceAllowlistRegen) decides how to treat "don't know" based on whether the file
// has anything worth protecting.
func checkAllowlistGitStatus(file string) allowlistGitStatus {
	dir := filepath.Dir(file)
	out, err := exec.Command("git", "-C", dir, "status", "--porcelain", "--", filepath.Base(file)).Output()
	if err != nil {
		return allowlistStatusUnknown
	}
	if strings.TrimSpace(string(out)) != "" {
		return allowlistStatusDirty
	}
	return allowlistStatusClean
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
		return fmt.Errorf("no pinned board coordinate in .ready/config.json — run: rd link <coord>")
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
		label, _ := cmd.Flags().GetString("label")
		from, _ := cmd.Flags().GetInt64("from")
		claim, _ := cmd.Flags().GetString("claim")
		// ready-58f: --all-boards fans the SAME owner-signed grant across EVERY board
		// this machine owns/has pinned locally — onboarding a key to N repos in ONE
		// act instead of N per-board grants. It does not require the CURRENT dir to be
		// a pinned board (you may run it from a projects umbrella dir).
		if allBoards, _ := cmd.Flags().GetBool("all-boards"); allBoards {
			projectsRoot, _ := cmd.Flags().GetString("projects-root")
			return runGrantAllBoards(projectsRoot, grantee, role, label, from, claim)
		}
		dir, native := nostrNativeProject()
		if !native {
			return fmt.Errorf("rd grant operates on a nostr-native project (kind-39301 role-grants) — run: rd link <coord> first")
		}
		return runNostrGrantRevoke(dir, grantee, role, label, from, claim)
	},
}

func init() {
	grantCmd.Flags().String("label", "", "human label carried in the grant content (used as the relay allowlist label)")
	grantCmd.Flags().Int64("from", 0, "effective-from unix seconds (0 = prospective / effective now)")
	grantCmd.Flags().String("claim", "", "one-use invite claim-nonce this grant consumes (from the joiner's `rd join` output); binds the nonce to this pubkey (single-use)")
	// ready-58f: onboard a key to many repos in one act.
	grantCmd.Flags().Bool("all-boards", false, "grant the role on EVERY board this machine owns/has pinned locally (default: only this project's board)")
	grantCmd.Flags().String("projects-root", "", "directory whose immediate subdirectories are scanned for locally-pinned boards (default: the parent of the current project dir); used with --all-boards")
	rootCmd.AddCommand(grantCmd)
}
