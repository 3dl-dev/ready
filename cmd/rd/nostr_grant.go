package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/campfire-net/ready/pkg/nostr"
	"github.com/campfire-net/ready/pkg/rdconfig"
	rdSync "github.com/campfire-net/ready/pkg/sync"
	"github.com/spf13/cobra"
)

// ONE SIGNED ACT admits or revokes an actor across BOTH the client trust set and the
// relay write-allowlist (ready-84e / BP-5). `rd grant` / `rd revoke` publish an
// owner-signed kind-39301 role-grant; `rd relay sync-allowlist` regenerates
// scripts/relay-policy/write-allowlist.json from those grants and pushes it to the
// live locked relays. The ready-266 plugin contract (binary {pubkey:label},
// mtime-reload, fail-closed) is UNCHANGED — only its feed becomes the signed log, so
// the client TrustSet and the relay file now share ONE source (design §4/§6, A3).
//
// ready-f58: grant/revoke are the top-level `rd grant`/`rd revoke` (cmd/rd/authz_nostr.go,
// cmd/rd/revoke.go); the allowlist regeneration is `rd relay sync-allowlist`. The old
// `rd nostr` grouping is gone — nostr is the substrate, not a user-typed mode.

// resolveBoardAuthorD resolves the (boardAuthor, boardD) a grant binds to. It prefers
// the PINNED board coordinate in .ready/config.json (30301:<owner>:<boardD>) — the
// authoritative source once pinned — and otherwise falls back to (signerPubkey,
// projectPrefix), the owner signing their own board (pre-pin behaviour). A grant MUST
// name a board; an empty resolution is an error the caller surfaces.
func resolveBoardAuthorD(dir, signerPubkey string) (boardAuthor, boardD string, err error) {
	if pin := nostrPinnedBoard(dir); pin != "" {
		owner, d, ok := rdSync.ParseBoardCoord(pin)
		if !ok {
			return "", "", fmt.Errorf("pinned board coordinate %q is malformed (want 30301:<owner>:<boardD>)", pin)
		}
		return owner, d, nil
	}
	d := projectPrefix(dir)
	if d == "" {
		return "", "", fmt.Errorf("cannot resolve board d from project dir %q; pin a board with 'rd pin-board'", dir)
	}
	return signerPubkey, d, nil
}

// publishRoleGrant is the shared body of grant/revoke: it builds an owner-signed
// 39301 grant for grantee/role, enforces the escalation cap client-side (fail fast —
// DeriveLevels also ignores a cap-violating grant, but refusing to publish it keeps
// the log clean), appends it to the local authoritative log, and best-effort
// publishes it to the write relays.
func publishRoleGrant(grantee, role, label string, from int64, claim string) error {
	if len(grantee) != 64 || !isHex(grantee) {
		return fmt.Errorf("grantee %q is not a valid pubkey: must be a 64-character hex string", grantee)
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
	dir, _ := readyProjectDir()
	signer := pub.Key.PubKeyHex()
	boardAuthor, boardD, err := resolveBoardAuthorD(dir, signer)
	if err != nil {
		return err
	}
	// Escalation cap (design §3), enforced client-side as a FAIL-FAST mirror of the
	// read-side rule (MED-6): rd.MayGrant derives the operator levels from the local
	// log and applies the SAME check DeriveLevels/signerMayGrant enforce at the
	// projection seam — only the board author may mint maintainer/owner; a maintainer
	// may grant contributor/revoked but NOT revoke/downgrade the owner (irrevocable)
	// or a peer maintainer; a contributor may grant nothing. DeriveLevels also ignores
	// a cap-violating grant, but refusing to PUBLISH it keeps the log clean and gives
	// the operator a clear error instead of a silently-dropped grant.
	events, err := pub.Log.ReadAll()
	if err != nil {
		return fmt.Errorf("reading local log for escalation-cap check: %w", err)
	}
	if !rdSync.MayGrant(events, boardAuthor, boardD, signer, grantee, role) {
		return fmt.Errorf("escalation cap: signer %s may not grant role %q to %s on board %s "+
			"(only the owner may mint maintainer/owner or revoke the owner/a maintainer; "+
			"a contributor may grant nothing)", signer, role, grantee, rdSync.BoardCoord(boardAuthor, boardD))
	}
	// SINGLE-USE CLAIM (ready-ce0): when --claim binds this grant to an invite
	// claim-nonce, refuse client-side (fail fast, clean log) if that nonce is already
	// bound to a DIFFERENT self-minted pubkey. Derivation enforces the same
	// one-claim-nonce-per-pubkey rule at projection (defense in depth), so a second
	// grant reusing a leaked claim for another key never takes effect regardless.
	if claim != "" {
		if bound, ok := rdSync.ClaimGrantee(events, boardAuthor, boardD, claim); ok && bound != grantee {
			return fmt.Errorf("invite claim-nonce %s is already consumed by pubkey %s — one claim-nonce admits exactly one key; "+
				"the second joiner needs a fresh `rd invite` token", claim, bound)
		}
	}
	spec := rdSync.RoleGrantSpec{
		BoardD:      boardD,
		BoardAuthor: boardAuthor,
		Grantee:     grantee,
		Role:        role,
		From:        from,
		Label:       label,
		Claim:       claim,
	}
	// Stamp with a strictly-monotonic created_at (max(now, newest+1)) SCOPED to THIS
	// grantee's (board,grantee) grant slot (ready-be1), NOT a bare time.Now() and NOT
	// the log-wide newest: a grant and a subsequent revoke of the SAME key issued within
	// one wall-clock second would otherwise share created_at and resolve by the id
	// tie-break, so a revoke could no-op against the grant it means to supersede
	// (design §3 NOTE-B). Per-grantee-scoped stamping restores intent order for
	// sequential same-machine authz writes without the log-wide future-drift that let an
	// unrelated grant burst inflate a fresh grant's created_at and beat a genuinely-later
	// cross-machine revoke (the ready-be1 lost-revoke) — cross-machine convergence
	// (the (created_at,id) key) is unchanged.
	ev, err := rdSync.BuildRoleGrantEvent(pub.Key, spec, nostrNextCreatedAt(pub.Log, rdSync.GrantDriftScope(boardD, grantee)))
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
	fmt.Printf("published role-grant: grantee=%s role=%s board=%s\n", grantee, role, rdSync.BoardCoord(boardAuthor, boardD))
	fmt.Printf("  event id=%s relay-accepted=%v\n", ev.ID, anyRelay)
	if res.Buffered {
		fmt.Println("  (reached no relay; buffered to nostr-pending.jsonl — durable in local log)")
	}
	return nil
}

// ready-f58: `rd nostr grant` / `rd nostr revoke` are GONE — they duplicated the
// canonical top-level `rd grant` / `rd revoke` (ready-477, cmd/rd/authz_nostr.go and
// cmd/rd/revoke.go), which publish the same owner-signed kind-39301 role-grant and
// regenerate the relay write-allowlist in one act. The retroactive-repudiation
// `--from` (and `--label`) that used to be unique to `rd nostr revoke` were migrated
// onto the top-level `rd revoke`. The shared body is publishRoleGrant (above).

// nostrPinBoardCmd establishes the pinned authoritative board coordinate in
// .ready/config.json (BP-3's pin, activated for this project — design DONE#3). Once
// pinned, the nostr projection rejects foreign-board cards and derives graded
// operator levels for THIS board. Default owner = the loaded owner key; default
// boardD = the project prefix.
var nostrPinBoardCmd = &cobra.Command{
	Use:   "pin-board",
	Short: "Pin this project's authoritative board coordinate (30301:<owner>:<boardD>) in .ready/config.json",
	RunE: func(cmd *cobra.Command, args []string) error {
		dir, ok := readyProjectDir()
		if !ok {
			return fmt.Errorf("no .ready project directory found")
		}
		force, _ := cmd.Flags().GetBool("force")
		if !force {
			if _, _, campfireOK := projectRoot(); campfireOK {
				return fmt.Errorf(".campfire/root exists — this project is campfire-backed; " +
					"pinning a nostr board here would silently orphan the existing campfire " +
					"history (all future reads/writes would resolve only from the nostr " +
					"projection). Run 'rd migrate' to move history to nostr instead, or pass " +
					"--force to pin anyway (existing campfire history will become invisible)")
			}
		}
		owner, _ := cmd.Flags().GetString("owner")
		boardD, _ := cmd.Flags().GetString("board-d")
		if owner == "" {
			k, err := nostrKey()
			if err != nil {
				return err
			}
			owner = k.PubKeyHex()
		}
		if len(owner) != 64 || !isHex(owner) {
			return fmt.Errorf("owner %q is not a valid pubkey (64 hex chars)", owner)
		}
		if boardD == "" {
			boardD = projectPrefix(dir)
		}
		if boardD == "" {
			return fmt.Errorf("cannot resolve board d; pass --board-d")
		}
		coord := rdSync.BoardCoord(owner, boardD)
		cfg, err := rdconfig.LoadSyncConfig(dir)
		if err != nil {
			return err
		}
		cfg.Board = coord
		if err := rdconfig.SaveSyncConfig(dir, cfg); err != nil {
			return err
		}
		fmt.Printf("pinned board: %s\n  (.ready/config.json)\n", coord)
		return nil
	},
}

// --- relay write-allowlist regeneration + push (sync-allowlist) ---

// readAllowlistFile reads a {pubkey:label} JSON allowlist file. A missing file is an
// empty map (not an error) so a first run has an empty on-disk baseline.
func readAllowlistFile(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parsing allowlist %s: %w", path, err)
	}
	if m == nil {
		m = map[string]string{}
	}
	return m, nil
}

// writeAllowlistFile writes a {pubkey:label} map as stable (sorted-key) 2-space JSON —
// byte-identical for identical content so it is safe to commit and diff, and so a
// no-op regeneration produces no spurious churn.
func writeAllowlistFile(path string, m map[string]string) error {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("{\n")
	for i, k := range keys {
		kb, _ := json.Marshal(k)
		vb, _ := json.Marshal(m[k])
		b.WriteString("  ")
		b.Write(kb)
		b.WriteString(": ")
		b.Write(vb)
		if i < len(keys)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	b.WriteString("}\n")
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
	}
	return os.WriteFile(path, []byte(b.String()), 0644)
}

// fetchLiveAllowlist reads the CURRENT allowlist from a relay VM via ssh, so the diff
// baseline reflects what is ACTUALLY enforced right now (including any drifted /
// externally-managed key the repo file does not track). This is what makes the
// no-lockout guard sound against the live relays.
func fetchLiveAllowlist(user, relay, remotePath string) (map[string]string, error) {
	out, err := exec.Command("ssh",
		"-o", "StrictHostKeyChecking=no", "-o", "ConnectTimeout=8",
		user+"@"+relay, "cat "+remotePath).Output()
	if err != nil {
		return nil, fmt.Errorf("ssh %s@%s cat %s: %w", user, relay, remotePath, err)
	}
	var m map[string]string
	if err := json.Unmarshal(out, &m); err != nil {
		return nil, fmt.Errorf("parsing live allowlist from %s: %w", relay, err)
	}
	return m, nil
}

// pushAllowlist ships localFile to a relay VM and sudo-installs it to remotePath. No
// strfry restart: the writePolicy plugin reloads on mtime change (fail-closed), so
// the new allowlist takes effect within one event without dropping the relay.
func pushAllowlist(user, relay, localFile, remotePath string) error {
	stage := "/tmp/rd-write-allowlist.json"
	if out, err := exec.Command("scp",
		"-o", "StrictHostKeyChecking=no", "-o", "ConnectTimeout=8",
		localFile, user+"@"+relay+":"+stage).CombinedOutput(); err != nil {
		return fmt.Errorf("scp to %s: %w: %s", relay, err, out)
	}
	remoteCmd := fmt.Sprintf("sudo install -m 0644 %s %s && rm -f %s && echo installed", stage, remotePath, stage)
	if out, err := exec.Command("ssh",
		"-o", "StrictHostKeyChecking=no", "-o", "ConnectTimeout=8",
		user+"@"+relay, remoteCmd).CombinedOutput(); err != nil {
		return fmt.Errorf("ssh install on %s: %w: %s", relay, err, out)
	}
	return nil
}

// defaultAllowlistFile resolves the version-controlled relay allowlist under the git
// repo root, so `rd relay sync-allowlist` regenerates the same file lock-relays.sh
// ships. Falls back to a repo-relative path when the git query fails.
func defaultAllowlistFile() string {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err == nil {
		root := strings.TrimSpace(string(out))
		if root != "" {
			return filepath.Join(root, "scripts", "relay-policy", "write-allowlist.json")
		}
	}
	return filepath.Join("scripts", "relay-policy", "write-allowlist.json")
}

var nostrSyncAllowlistCmd = &cobra.Command{
	Use:   "sync-allowlist",
	Short: "Regenerate the relay write-allowlist from role-grants and (with --apply) push it to the relays",
	Long: `Regenerate scripts/relay-policy/write-allowlist.json from the signed role-grants
in the local log: admitted = { board author } ∪ { non-revoked grantees }. Revoked
keys are pruned. The relay plugin contract is unchanged — only its FEED becomes the
signed log, so rd's client trust set and the relay file share ONE source.

NO-LOCKOUT INVARIANT: a currently-admitted key is removed IFF it has an explicit
role=revoked grant. A currently-admitted key with NO grant (e.g. a third-party
tenant sharing the relay) is PRESERVED and reported, never silently dropped. If a key
would be removed WITHOUT a revoke grant, the apply path FAILS CLOSED.

By default this is a DRY RUN: it prints the added/removed/preserved diff and writes
nothing. Pass --apply to write the file and scp/ssh it to the relays. Review the
dry-run diff before applying.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		apply, _ := cmd.Flags().GetBool("apply")
		file, _ := cmd.Flags().GetString("file")
		ownerLabel, _ := cmd.Flags().GetString("owner-label")
		relaysCSV, _ := cmd.Flags().GetString("relays")
		user, _ := cmd.Flags().GetString("relay-user")
		remotePath, _ := cmd.Flags().GetString("remote-path")
		noFetch, _ := cmd.Flags().GetBool("no-fetch")

		if file == "" {
			file = defaultAllowlistFile()
		}
		dir, ok := readyProjectDir()
		if !ok {
			return fmt.Errorf("no .ready project directory found")
		}
		k, err := nostrKey()
		if err != nil {
			return err
		}
		boardAuthor, boardD, err := resolveBoardAuthorD(dir, k.PubKeyHex())
		if err != nil {
			return err
		}
		if ownerLabel == "" {
			ownerLabel = "board author (owner) — bootstrap trust root"
		}
		log := rdSync.NewNostrLog(rdSync.NostrLogPath(dir))
		events, err := log.ReadAll()
		if err != nil {
			return err
		}

		relays := splitCSV(relaysCSV)

		// Baseline = what is ACTUALLY enforced now. Prefer the live relay allowlists
		// (captures drift like an externally-managed key); fall back to the on-disk
		// file. Union across relays so a key admitted on either relay is preserved.
		baseline := map[string]string{}
		fetched := false
		if !noFetch && len(relays) > 0 {
			for _, r := range relays {
				live, ferr := fetchLiveAllowlist(user, r, remotePath)
				if ferr != nil {
					fmt.Fprintf(os.Stderr, "warning: could not fetch live allowlist from %s: %v\n", r, ferr)
					continue
				}
				fetched = true
				for pk, lbl := range live {
					if _, exists := baseline[pk]; !exists {
						baseline[pk] = lbl
					}
				}
			}
		}
		if !fetched {
			onDisk, derr := readAllowlistFile(file)
			if derr != nil {
				return derr
			}
			for pk, lbl := range onDisk {
				baseline[pk] = lbl
			}
			fmt.Fprintln(os.Stderr, "note: using on-disk allowlist as baseline (live relay fetch skipped/failed)")
		}

		plan := rdSync.PlanAllowlist(events, boardAuthor, boardD, ownerLabel, baseline)

		// Print the reviewable diff.
		fmt.Printf("relay write-allowlist regeneration (board author %s)\n", boardAuthor)
		fmt.Printf("  baseline: %d key(s)  ->  final: %d key(s)\n", len(baseline), len(plan.Final))
		printKeyList("ADD", plan.Added, plan.Final)
		printKeyList("REMOVE", plan.Removed, baseline)
		printKeyList("PRESERVE (admitted, no rd grant — will NOT be removed)", plan.Preserved, baseline)

		if len(plan.LockoutViolations) > 0 {
			fmt.Fprintf(os.Stderr, "\nERROR: would remove currently-admitted key(s) with NO revoke grant — refusing (fail closed):\n")
			for _, pk := range plan.LockoutViolations {
				fmt.Fprintf(os.Stderr, "  %s\n", pk)
			}
			return fmt.Errorf("no-lockout guard tripped: %d key(s) would be locked out", len(plan.LockoutViolations))
		}

		if !apply {
			fmt.Println("\n(dry run — nothing written or pushed; re-run with --apply after reviewing the diff)")
			if jsonOutput {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(plan)
			}
			return nil
		}

		if err := writeAllowlistFile(file, plan.Final); err != nil {
			return err
		}
		fmt.Printf("\nwrote %s (%d keys)\n", file, len(plan.Final))
		var pushErrs []string
		for _, r := range relays {
			if perr := pushAllowlist(user, r, file, remotePath); perr != nil {
				pushErrs = append(pushErrs, perr.Error())
				fmt.Fprintf(os.Stderr, "  push %s FAILED: %v\n", r, perr)
				continue
			}
			fmt.Printf("  pushed to %s:%s\n", r, remotePath)
		}
		if len(pushErrs) > 0 {
			return fmt.Errorf("relay push failed for %d relay(s): %s", len(pushErrs), strings.Join(pushErrs, "; "))
		}
		fmt.Println("relays updated (plugin mtime-reloads; no restart needed)")
		return nil
	},
}

func printKeyList(label string, keys []string, labels map[string]string) {
	if len(keys) == 0 {
		return
	}
	fmt.Printf("  %s (%d):\n", label, len(keys))
	for _, pk := range keys {
		fmt.Printf("    %s  %s\n", pk, labels[pk])
	}
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func init() {
	nostrPinBoardCmd.Flags().String("owner", "", "owner pubkey hex (default: the loaded owner key)")
	nostrPinBoardCmd.Flags().String("board-d", "", "board d identifier (default: the project prefix)")
	nostrPinBoardCmd.Flags().Bool("force", false, "pin the board even though a legacy project root exists (orphans existing legacy history — prefer 'rd migrate')")
	nostrSyncAllowlistCmd.Flags().Bool("apply", false, "write the file and push it to the relays (default: dry-run diff only)")
	nostrSyncAllowlistCmd.Flags().String("file", "", "local allowlist json to (re)generate (default: <repo>/scripts/relay-policy/write-allowlist.json)")
	nostrSyncAllowlistCmd.Flags().String("owner-label", "", "label for the bootstrap owner key")
	nostrSyncAllowlistCmd.Flags().String("relays", "192.168.2.40,192.168.2.41", "comma-separated relay hosts to fetch baseline from and push to")
	nostrSyncAllowlistCmd.Flags().String("relay-user", "baron", "ssh user for the relay VMs")
	nostrSyncAllowlistCmd.Flags().String("remote-path", "/etc/strfry/write-allowlist.json", "path the strfry writePolicy plugin reads on each relay")
	nostrSyncAllowlistCmd.Flags().Bool("no-fetch", false, "do not fetch the live relay allowlist for the baseline; use the on-disk --file instead")
	// ready-f58: pin-board promoted to top-level `rd pin-board`; sync-allowlist moved
	// under the new `rd relay` namespace. Both `rd nostr` hosts are gone.
	rootCmd.AddCommand(nostrPinBoardCmd)
	relayCmd.AddCommand(nostrSyncAllowlistCmd)
}
