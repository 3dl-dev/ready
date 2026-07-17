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

	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/rdconfig"
	rdSync "github.com/3dl-dev/ready/pkg/sync"
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
		return "", "", fmt.Errorf("cannot resolve board d from project dir %q — run: rd link <coord>", dir)
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
	// Confidential-by-default (ready-216): on a confidential board an owner grant
	// that confers read access carries the current-epoch CEK + the LTK NIP-44-wrapped
	// to the grantee, so `rd grant` alone lets the member read. No-op on a plaintext
	// board, for a revoke, or for a non-owner signer (only owner-signed CEK counts).
	wCEK, cekEpoch, wLTK, kerr := confidentialGrantKeys(dir, pub, boardAuthor, boardD, grantee, role)
	if kerr != nil {
		return fmt.Errorf("wrapping board CEK for grant: %w", kerr)
	}
	spec.WrappedCEK = wCEK
	spec.CEKEpoch = cekEpoch
	spec.WrappedLTK = wLTK
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
	if res.Rejected {
		fmt.Fprintln(os.Stderr, "  WARNING: the role-grant event was permanently rejected by a relay and dead-lettered to nostr-rejected.jsonl — NOT retried; inspect and fix.")
	}
	return nil
}

// --- ready-58f: `rd grant --all-boards` (fan one grant across every local board) ---

// localBoard is a locally-pinned board this machine owns: the project directory whose
// .ready/config.json pins it, plus the board coordinate (30301:<owner>:<boardD>).
type localBoard struct {
	dir   string
	coord string
}

// discoverLocalOwnedBoards enumerates the boards this machine owns/has pinned locally
// by scanning the immediate subdirectories of projectsRoot for a .ready/config.json
// that pins a board coordinate OWNED by ownerPubkey. There is no separate board
// registry — the pin in each repo's SyncConfig IS the enumeration source. Dirs with no
// pinned board, or a board owned by a foreign key (which the escalation cap would
// reject anyway), are skipped. Results are sorted by dir for a stable grant order.
func discoverLocalOwnedBoards(projectsRoot, ownerPubkey string) ([]localBoard, error) {
	entries, err := os.ReadDir(projectsRoot)
	if err != nil {
		return nil, err
	}
	var out []localBoard
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(projectsRoot, e.Name())
		cfg, cerr := rdconfig.LoadSyncConfig(dir)
		if cerr != nil || cfg.Board == "" {
			continue
		}
		owner, _, ok := rdSync.ParseBoardCoord(cfg.Board)
		if !ok || owner != ownerPubkey {
			continue
		}
		out = append(out, localBoard{dir: dir, coord: cfg.Board})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].dir < out[j].dir })
	return out, nil
}

// runGrantAllBoards fans the SAME owner-signed grant across EVERY board this machine
// owns/has pinned locally (onboarding a key to N repos in ONE act, vs. N per-board
// grants). projectsRoot defaults to the parent of the current project dir (its sibling
// repos). Each board is granted by chdir'ing into its repo and reusing the full
// single-board pipeline (runNostrGrantRevoke): the grant is published into THAT repo's
// authoritative log + write relays and its relay write-allowlist is regenerated —
// behaviour identical to running `rd grant` in each repo. A per-board failure is
// collected and reported without aborting the remaining boards; the grant is durable in
// every repo whose publish succeeded regardless.
func runGrantAllBoards(projectsRoot, grantee, role, label string, from int64, claim string) error {
	if projectsRoot == "" {
		dir, ok := readyProjectDir()
		if !ok {
			return fmt.Errorf("--all-boards needs a projects root: run inside a project (its parent directory is scanned) or pass --projects-root")
		}
		projectsRoot = filepath.Dir(dir)
	}
	k, err := nostrKey()
	if err != nil {
		return err
	}
	owner := k.PubKeyHex()
	boards, err := discoverLocalOwnedBoards(projectsRoot, owner)
	if err != nil {
		return fmt.Errorf("discovering local boards under %s: %w", projectsRoot, err)
	}
	if len(boards) == 0 {
		return fmt.Errorf("no locally-pinned boards owned by %s found under %s — run: rd link <coord> in each board's repo first (or pass --projects-root)", shortKey(owner), projectsRoot)
	}
	origCwd, err := os.Getwd()
	if err != nil {
		return err
	}
	defer func() { _ = os.Chdir(origCwd) }()

	granted := 0
	var failures []string
	for _, b := range boards {
		if err := os.Chdir(b.dir); err != nil {
			failures = append(failures, fmt.Sprintf("%s (%s): chdir: %v", b.coord, b.dir, err))
			continue
		}
		if err := runNostrGrantRevoke(b.dir, grantee, role, label, from, claim); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", b.coord, err))
			continue
		}
		granted++
	}
	fmt.Printf("\n--all-boards: granted role %q to %s on %d/%d local board(s)\n", role, grantee, granted, len(boards))
	if len(failures) > 0 {
		return fmt.Errorf("grant failed on %d of %d board(s): %s", len(failures), len(boards), strings.Join(failures, "; "))
	}
	return nil
}

// ready-f58: `rd nostr grant` / `rd nostr revoke` are GONE — they duplicated the
// canonical top-level `rd grant` / `rd revoke` (ready-477, cmd/rd/authz_nostr.go and
// cmd/rd/revoke.go), which publish the same owner-signed kind-39301 role-grant and
// regenerate the relay write-allowlist in one act. The retroactive-repudiation
// `--from` (and `--label`) that used to be unique to `rd nostr revoke` were migrated
// onto the top-level `rd revoke`. The shared body is publishRoleGrant (above).

// runLinkOrPinBoard is the shared body of `rd link` and its hidden deprecated
// alias `rd pin-board` (ready-8ff: the binding command is 'rd link' now — do
// NOT delete pin-board this release, teammates' muscle-memory/scripts still
// call it). Both commands register the SAME flags (--owner, --board-d,
// --force) and point their RunE at this one function, so the two names are
// guaranteed to behave identically — there is no drift between them.
//
// Bare invocation (no positional board-coord, no --owner/--board-d flag
// VALUES) PRINTS the currently-linked board instead of erroring or re-linking
// — a no-arg `rd link` is a status query, not a mutation. Otherwise it binds
// this repo to a board coordinate (from the positional arg, or from --owner/
// --board-d, or a bare owner-key default), same as pin-board always did.
//
// "Bare" is judged by flag VALUE (empty string), not cobra's Changed() bit:
// Changed() latches true the first time a flag is ever Set() (including
// resetting it back to "" in test cleanup) and never un-latches, which would
// make a later bare invocation of this SAME long-lived *cobra.Command
// wrongly skip the status-print path. A real CLI invocation is a fresh
// process per call so this only matters for tests reusing the package-level
// command vars, but checking the value keeps both cases correct.
func runLinkOrPinBoard(cmd *cobra.Command, args []string) error {
	dir, ok := readyProjectDir()
	if !ok {
		return fmt.Errorf("no .ready project directory found — run: rd init")
	}

	ownerFlag, _ := cmd.Flags().GetString("owner")
	boardDFlag, _ := cmd.Flags().GetString("board-d")
	if len(args) == 0 && ownerFlag == "" && boardDFlag == "" {
		return printLinkedBoard(dir)
	}

	force, _ := cmd.Flags().GetBool("force")
	if !force {
		if _, _, campfireOK := projectRoot(); campfireOK {
			return fmt.Errorf(".campfire/root exists — this project is campfire-backed. " +
				"If you are a follower adopting an existing nostr board (an owner or " +
				"teammate already linked it and invited you), pass --force to 'rd link' and " +
				"adopt that board — this is the normal follower path here, not a rare " +
				"recovery op. --force does not touch or orphan the campfire history; it " +
				"only switches this project's reads/writes to the linked nostr board " +
				"coordinate. Do not run 'rd migrate' to join someone else's board: migrate " +
				"re-emits this project's items signed under YOUR key, which forks a new " +
				"board instead of joining the existing one")
		}
	}
	owner, boardD := ownerFlag, boardDFlag
	if len(args) == 1 {
		o, d, ok := rdSync.ParseBoardCoord(args[0])
		if !ok {
			return fmt.Errorf("board coordinate %q is malformed (want 30301:<owner>:<boardD>)", args[0])
		}
		owner, boardD = o, d
	}
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
	// ready-f12: rd link is the follow/link fallback — it too WRITES the
	// committed binding so a follower who adopts a board leaves a tracked
	// .ready/board.json for the next clone. Carry the project name + relays
	// forward from the existing config when the binding does not already set them.
	binding, err := rdconfig.LoadBoardBinding(dir)
	if err != nil {
		return err
	}
	binding.Board = coord
	if binding.ProjectName == "" {
		binding.ProjectName = cfg.ProjectName
	}
	if len(binding.RelayEndpoints) == 0 {
		binding.RelayEndpoints = cfg.RelayEndpoints
	}
	if err := rdconfig.SaveBoardBinding(dir, binding); err != nil {
		return err
	}
	fmt.Printf("linked board: %s\n  (.ready/config.json + .ready/board.json)\n", coord)
	return nil
}

// printLinkedBoard reports the board coordinate currently pinned in
// .ready/config.json for `rd link` with no arguments — a status query.
func printLinkedBoard(dir string) error {
	pin := nostrPinnedBoard(dir)
	if pin == "" {
		fmt.Println("not linked — run rd follow <owner>")
		return nil
	}
	owner, boardD, ok := rdSync.ParseBoardCoord(pin)
	if !ok {
		fmt.Printf("this repo is linked to board: %s\n", pin)
		return nil
	}
	fmt.Printf("this repo is linked to board: %s (owner %s)\n", boardD, owner[:8])
	return nil
}

// nostrLinkCmd is the top-level `rd link` — the binding command that establishes
// the pinned authoritative board coordinate in .ready/config.json (BP-3's pin,
// activated for this project — design DONE#3; renamed from pin-board by
// ready-8ff). Once linked, the nostr projection rejects foreign-board cards and
// derives graded operator levels for THIS board. Default owner = the loaded
// owner key; default boardD = the project prefix. With no arguments, prints the
// currently-linked board instead of mutating anything.
var nostrLinkCmd = &cobra.Command{
	Use:   "link [board-coord]",
	Short: "Link this repo to its authoritative board (30301:<owner>:<boardD>), or print the currently-linked board",
	Long: `With a board-coord argument (or --owner/--board-d), binds this repo's
.ready/config.json to that board coordinate — the nostr projection then rejects
foreign-board cards and derives graded operator levels for THIS board.

With NO arguments, prints the board this repo is currently linked to (or
'not linked' if none) instead of mutating anything.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runLinkOrPinBoard,
}

// nostrPinBoardCmd is the HIDDEN deprecated alias for `rd link` (ready-8ff).
// pin-board was the original name; it is not deleted this release — existing
// scripts/muscle-memory keep working — but 'rd link' is now the documented,
// visible command.
var nostrPinBoardCmd = &cobra.Command{
	Use:    "pin-board [board-coord]",
	Hidden: true,
	Short:  "Deprecated alias for 'rd link'",
	Args:   cobra.MaximumNArgs(1),
	RunE:   runLinkOrPinBoard,
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
		if len(relays) == 0 {
			fmt.Println("no --relays given: wrote the local file only, pushed to nothing.")
			return nil
		}
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
	// ready-8ff: `rd link` and its hidden deprecated alias `rd pin-board` share
	// runLinkOrPinBoard, so both need the SAME flags registered on their OWN
	// flag sets (cobra flags are per-command; RunE reads whichever command was
	// actually invoked via its `cmd` parameter).
	linkForceHelp := "link the board even though a legacy project root exists — the follower path for adopting an existing nostr board (does not touch or orphan the legacy history; do not run 'rd migrate' to join someone else's board, it forks a new one under your key)"
	nostrLinkCmd.Flags().String("owner", "", "owner pubkey hex (default: the loaded owner key)")
	nostrLinkCmd.Flags().String("board-d", "", "board d identifier (default: the project prefix)")
	nostrLinkCmd.Flags().Bool("force", false, linkForceHelp)
	nostrPinBoardCmd.Flags().String("owner", "", "owner pubkey hex (default: the loaded owner key)")
	nostrPinBoardCmd.Flags().String("board-d", "", "board d identifier (default: the project prefix)")
	nostrPinBoardCmd.Flags().Bool("force", false, linkForceHelp)
	nostrSyncAllowlistCmd.Flags().Bool("apply", false, "write the file and push it to the relays (default: dry-run diff only)")
	nostrSyncAllowlistCmd.Flags().String("file", "", "local allowlist json to (re)generate (default: <repo>/scripts/relay-policy/write-allowlist.json)")
	nostrSyncAllowlistCmd.Flags().String("owner-label", "", "label for the bootstrap owner key")
	nostrSyncAllowlistCmd.Flags().String("relays", "", "comma-separated relay hosts to fetch baseline from and push to (required to push; empty = regenerate the local file only)")
	nostrSyncAllowlistCmd.Flags().String("relay-user", "baron", "ssh user for the relay VMs")
	nostrSyncAllowlistCmd.Flags().String("remote-path", "/etc/strfry/write-allowlist.json", "path the strfry writePolicy plugin reads on each relay")
	nostrSyncAllowlistCmd.Flags().Bool("no-fetch", false, "do not fetch the live relay allowlist for the baseline; use the on-disk --file instead")
	// ready-8ff: `rd link` is the primary binding command; `rd pin-board` stays
	// registered as a HIDDEN deprecated alias (not deleted this release).
	// sync-allowlist lives under the `rd relay` namespace. Both `rd nostr` hosts
	// are gone (ready-f58).
	rootCmd.AddCommand(nostrLinkCmd)
	rootCmd.AddCommand(nostrPinBoardCmd)
	relayCmd.AddCommand(nostrSyncAllowlistCmd)
}
