package main

// `rd follow <owner-identity>` — join ALL of an owner's boards in ONE command,
// with NO 64-hex coordinate to copy (ready-636).
//
// `rd join` (nostr_invite.go) SELF-MINTS a fresh key and adopts ONE board from a
// token. `rd follow` is the KEEP-YOUR-IDENTITY, ADOPT-EVERYTHING counterpart the
// join owner-key guard points at (edge #1): it keeps this machine's existing key
// (minting only if none exists — NEVER overwriting an owner key), DISCOVERS every
// board the owner published (their signed kind-30301 board events on the relays),
// and for EACH board: binds it non-destructively via the `rd link` path (writes a
// committed .ready/board.json), pulls the board's full history into that project's
// authoritative log, and refreshes this machine's person-alias so the owner can
// grant it. It prints this machine's pubkey once with the exact
// `rd grant --all-boards <pubkey>` line for the owner to run — no coordinate is
// ever printed for the operator to retype.
//
// `<owner-identity>` is resolved to the owner pubkey(s) + relays from any of:
//   - an EMAIL handle (baron@3dl.dev) — resolved through the local party resolver
//     (pkg/identity, kind 39302): the owner's self-signed alias maps email->keys,
//     honored because the follower already trusts that key (v1 single-operator
//     trust: the machine key is the trust root, so following YOUR OWN boards on a
//     fresh checkout resolves; a third party's boards need an npub/token/hex);
//   - an `rd1_` one-paste invite TOKEN — its board coordinate names the owner and
//     it carries the relay set (all of the owner's boards are then discovered, not
//     just the token's one board);
//   - an `npub1...` bech32 pubkey, or a bare 64-hex pubkey.

import (
	"bufio"
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/3dl-dev/ready/pkg/identity"
	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/rdconfig"
	rdSync "github.com/3dl-dev/ready/pkg/sync"
	"github.com/spf13/cobra"
)

// defaultFollowEmail is the party handle registered for this machine's key when
// --email is not given. It matches the operator this rd install belongs to.
const defaultFollowEmail = "baron@3dl.dev"

// followConfirmThreshold is the discovered-board-count above which a
// no-`--board` `rd follow` REQUIRES an explicit confirmation before binding
// (ready-4c9c). Below or at the threshold, following is left frictionless (this
// is also why TestFollow_BindsAllOwnerBoardsKeepingKey's 3-board fixture binds
// with no --all/--yes and no prompt). Above it — the "followed 88 throwaway
// fixture boards over 6 minutes with no preview" footgun this const fixes — the
// operator must see the count+names and either confirm interactively or pass
// --all/--yes.
const followConfirmThreshold = 3

// followFetch fetches events matching filter from relays, merged across relays and
// de-duplicated by event id. It is a package-level var so the deterministic
// integration test replaces it with a local seeded-log reader — the same
// no-live-relay seam pattern as relayInviteMedium.fetchFn (ready-6d5 flaky
// family). A down relay is skipped best-effort; an all-relay failure surfaces the
// first error so a genuinely offline follow is reported, not silently empty.
var followFetch = func(ctx context.Context, relays []string, filter map[string]any) ([]*nostr.Event, error) {
	seen := map[string]*nostr.Event{}
	var firstErr error
	for _, r := range relays {
		rctx, cancel := context.WithTimeout(ctx, nostr.DefaultTimeout)
		evs, err := nostr.FetchMany(rctx, r, filter)
		cancel()
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		for _, e := range evs {
			if e != nil {
				seen[e.ID] = e
			}
		}
	}
	out := make([]*nostr.Event, 0, len(seen))
	for _, e := range seen {
		out = append(out, e)
	}
	if len(out) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return out, nil
}

// followConfirm previews the discovered board names and asks the operator to
// confirm before binding all of them. It is a package-level var (same seam
// pattern as followFetch) so the unit test can assert the >threshold gate
// blocks without a real stdin/tty, and so a future interactive test could
// inject a scripted "yes". Non-interactive stdin (scripts, CI, this test
// harness) never auto-approves — isInteractive() gates the prompt itself, so
// the only way to bind >threshold boards without a live human at a tty is
// --all/--yes.
var followConfirm = func(names []string) bool {
	if !isInteractive() {
		return false
	}
	fmt.Fprintln(os.Stderr)
	fmt.Fprintf(os.Stderr, "rd follow discovered %d boards:\n", len(names))
	for _, n := range names {
		fmt.Fprintf(os.Stderr, "  %s\n", n)
	}
	fmt.Fprint(os.Stderr, "Bind ALL of these boards into subdirectories? [y/N] ")
	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	line = strings.TrimSpace(strings.ToLower(line))
	return line == "y" || line == "yes"
}

// followOpts are the resolved inputs to runFollow. The command RunE fills them
// from flags + config; the integration test constructs them directly.
type followOpts struct {
	who    string   // owner-identity token: email | npub1 | rd1_ | 64-hex
	boardD string   // "" = discover all boards; non-empty = single board
	email  string   // party handle to (re)register for this machine's key
	root   string   // projects root under which one dir per board is created
	relays []string // relays to discover/import/backfill through
	all    bool     // --all/--yes: skip the >threshold confirmation, bind non-interactively
}

// followReport is the outcome runFollow returns for the command to print (and the
// test to assert on). It carries no coordinate the operator must retype.
type followReport struct {
	Pubkey       string            // this machine's pubkey (for the grant line)
	MintedKey    bool              // true only if no key existed and one was minted
	BoardDirs    map[string]string // boardD -> bound project dir (with board.json)
	Boards       []string          // bound board coordinates (info only)
	AliasEventID string            // the single person-alias published
}

var followCmd = &cobra.Command{
	Use:   "follow <owner-identity>",
	Short: "Join ALL of an owner's boards in one command (email / npub / rd1_ token) — no coordinate to copy",
	Long: `Join every board an owner published, keeping THIS machine's identity.

<owner-identity> is an email (baron@3dl.dev), an npub, or a one-paste rd1_ invite
token — rd resolves it to the owner's pubkey + relays, discovers every board that
owner published, and for each board binds it (committed .ready/board.json), pulls
full history, and refreshes this machine's person-alias.

Unlike 'rd join', follow NEVER re-mints your key: if this machine already owns a
board its key is kept unchanged. It prints your pubkey with the exact
'rd grant --all-boards <pubkey>' line to send the owner. No board coordinate is
ever printed for you to retype.

If the owner published more than a few boards, follow previews the discovered
count and names and asks you to confirm before binding any of them — pass
--all (or --yes) to skip that prompt for scripts/CI. --board always binds one
named board immediately with no prompt.

  rd follow baron@3dl.dev                 # all of the owner's boards
  rd follow baron@3dl.dev --board ready   # just one board
  rd follow baron@3dl.dev --all           # skip the confirmation prompt
  rd follow npub1... | rd follow rd1_...  # by pubkey or invite token`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			return fmt.Errorf("owner-identity required (rd follow <email|npub|rd1_token>)")
		}
		boardD, _ := cmd.Flags().GetString("board")
		email, _ := cmd.Flags().GetString("email")
		root, _ := cmd.Flags().GetString("root")
		allFlag, _ := cmd.Flags().GetBool("all")
		yesFlag, _ := cmd.Flags().GetBool("yes")
		if root == "" {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("resolving working directory: %w", err)
			}
			root = cwd
		}
		if email == "" {
			email = defaultFollowEmail
		}
		rep, err := runFollow(followOpts{
			who:    args[0],
			boardD: boardD,
			email:  email,
			root:   root,
			relays: nostrReadRelays(),
			all:    allFlag || yesFlag,
		})
		if err != nil {
			return err
		}
		printFollowReport(rep)
		return nil
	},
}

// runFollow is the testable core: keep-or-mint key, resolve the owner-identity,
// discover every board, bind + backfill each, publish one alias. It writes only
// under opts.root and $RD_HOME and reaches relays through followFetch / the
// Publisher, so the integration test drives it with a seeded local log and no
// network.
func runFollow(opts followOpts) (*followReport, error) {
	ctx := context.Background()
	rdHome := RDHome()

	// (1) KEEP-OR-MINT this machine's key. nostrKey() load-or-creates without ever
	// overwriting an existing key, so detecting existence first is enough to report
	// mint-vs-keep AND guarantee a pre-existing owner key is never re-minted.
	keyPath, err := nostr.ActorKeyPath(rdHome, rdActor())
	if err != nil {
		return nil, fmt.Errorf("resolving key path: %w", err)
	}
	minted := !fileExists(keyPath)
	k, err := nostrKey()
	if err != nil {
		return nil, err
	}
	self := k.PubKeyHex()

	// (2) DISCOVER snapshot: the owner's aliases (email resolution) + boards.
	snapshot, err := followFetch(ctx, opts.relays, map[string]any{
		"kinds": []int{identity.KindPersonAlias, rdSync.KindBoard},
	})
	if err != nil {
		return nil, fmt.Errorf("fetching owner boards from relays: %w", err)
	}

	// (3) Resolve the owner-identity to owner pubkey(s) (+ any token relays).
	ownerPubkeys, tokenRelays, err := resolveFollowTarget(opts.who, opts.email, snapshot, self)
	if err != nil {
		return nil, err
	}
	relays := dedupStrings(append(append([]string{}, opts.relays...), tokenRelays...))

	// (4) Enumerate every board the owner published (optionally one --board).
	boards := rdSync.DiscoverOwnerBoards(snapshot, ownerPubkeys, opts.boardD)
	if len(boards) == 0 {
		hint := "the owner may not have published any boards to these relays, or you don't trust their key"
		if opts.boardD != "" {
			hint = fmt.Sprintf("no board with d=%q was found for this owner", opts.boardD)
		}
		return nil, fmt.Errorf("rd follow: discovered no boards for %q — %s", opts.who, hint)
	}
	sort.Strings(boards)

	// (4b) CONFIRMATION GATE (ready-4c9c): a bare `rd follow <owner>` with no
	// --board discovers and binds EVERY board the owner published. Left
	// unguarded this silently dumped 88 throwaway fixture boards into cwd over
	// ~6 minutes with no preview, no count, and no progress output — it looked
	// hung. `--board <name>` names exactly one board and is scoped by
	// DiscoverOwnerBoards above already, so it never reaches this gate. Above
	// followConfirmThreshold boards, preview the count+names and REQUIRE
	// confirmation (interactive y/N via followConfirm, or --all/--yes to skip
	// it for scripts) before binding anything.
	if opts.boardD == "" && len(boards) > followConfirmThreshold && !opts.all {
		names := make([]string, 0, len(boards))
		for _, coord := range boards {
			if _, d, ok := rdSync.ParseBoardCoord(coord); ok {
				names = append(names, d)
			}
		}
		if !followConfirm(names) {
			return nil, fmt.Errorf("rd follow: discovered %d boards for %q — refusing to bind without confirmation:\n  %s\n"+
				"pass --all (or --yes) to bind all of them non-interactively, or --board <name> to bind just one",
				len(boards), opts.who, strings.Join(names, "\n  "))
		}
	}

	rep := &followReport{Pubkey: self, MintedKey: minted, BoardDirs: map[string]string{}, Boards: boards}

	// (5) For each board: bind (committed board.json) + pull full history + backfill.
	var primaryDir string
	total := len(boards)
	for i, coord := range boards {
		owner, boardD, ok := rdSync.ParseBoardCoord(coord)
		if !ok {
			continue
		}
		fmt.Fprintf(os.Stderr, "[%d/%d] binding %s...\n", i+1, total, boardD)
		dir := filepath.Join(opts.root, boardD)
		if err := bindFollowedBoard(dir, coord, boardD, relays); err != nil {
			return nil, fmt.Errorf("binding board %s: %w", boardD, err)
		}
		if err := importFollowedBoard(ctx, dir, coord, owner, boardD, snapshot, relays); err != nil {
			return nil, fmt.Errorf("importing board %s: %w", boardD, err)
		}
		rep.BoardDirs[boardD] = dir
		if primaryDir == "" {
			primaryDir = dir
		}
	}

	// (6) Refresh this machine's person-alias ONCE (published into the primary
	// board's log/relays). This is the write side of email->party resolution: it
	// lets the owner map this pubkey back to the operator.
	aliasID, err := publishFollowAlias(ctx, primaryDir, k, opts.email, relays)
	if err != nil {
		return nil, fmt.Errorf("publishing person-alias: %w", err)
	}
	rep.AliasEventID = aliasID

	return rep, nil
}

// resolveFollowTarget maps the owner-identity token to the owner pubkey(s) and any
// relays the token carries. Order: rd1_ token, npub, email (party resolver), bare
// 64-hex. A bare pubkey / npub / token names ONE owner key; an email may resolve
// to a MULTI-KEY party (every machine of that operator), whose boards are all
// discovered.
func resolveFollowTarget(who, email string, snapshot []*nostr.Event, self string) (ownerPubkeys, tokenRelays []string, err error) {
	switch {
	case strings.HasPrefix(who, nostrInviteTokenPrefix):
		p, derr := decodeNostrClaimToken(who)
		if derr != nil {
			return nil, nil, fmt.Errorf("invalid invite token: %w", derr)
		}
		owner, _, ok := rdSync.ParseBoardCoord(p.Board)
		if !ok {
			return nil, nil, fmt.Errorf("invite token has a malformed board coordinate")
		}
		return []string{owner}, p.Relays, nil

	case strings.HasPrefix(who, "npub1"):
		pub, derr := decodeNpub(who)
		if derr != nil {
			return nil, nil, derr
		}
		return []string{pub}, nil, nil

	case strings.Contains(who, "@"):
		r := identity.Resolve(snapshot, []string{self})
		keys, ok := r.KeysForParty(who)
		if !ok || len(keys) == 0 {
			_ = email
			return nil, nil, fmt.Errorf("rd follow: no trusted person-alias maps %q to a pubkey. "+
				"The owner must publish one with `rd identify --add-email %s`, and you must already "+
				"trust their key (follow your OWN boards, or use an npub / rd1_ token for someone else's)", who, who)
		}
		return keys, nil, nil

	case len(who) == 64 && isHex(who):
		return []string{who}, nil, nil

	default:
		return nil, nil, fmt.Errorf("rd follow: %q is not an email, npub, rd1_ token, or 64-hex pubkey", who)
	}
}

// bindFollowedBoard binds dir to coord non-destructively via the `rd link` write
// path: it load-modify-saves .ready/config.json AND writes the committed
// .ready/board.json (ready-f12) so a later clone resolves the board with no
// follow step. Existing sibling fields are preserved. It NEVER re-signs or forks
// the board — it only records the owner's coordinate this repo reads/writes.
func bindFollowedBoard(dir, coord, boardD string, relays []string) error {
	if err := os.MkdirAll(filepath.Join(dir, ".ready"), 0o755); err != nil {
		return fmt.Errorf("creating project dir: %w", err)
	}
	eps := relayEndpointsFrom(relays)

	cfg, err := rdconfig.LoadSyncConfig(dir)
	if err != nil {
		return err
	}
	cfg.Board = coord
	if cfg.ProjectName == "" {
		cfg.ProjectName = boardD
	}
	if len(cfg.RelayEndpoints) == 0 {
		cfg.RelayEndpoints = eps
	}
	if err := rdconfig.SaveSyncConfig(dir, cfg); err != nil {
		return err
	}

	binding, err := rdconfig.LoadBoardBinding(dir)
	if err != nil {
		return err
	}
	binding.Board = coord
	if binding.ProjectName == "" {
		binding.ProjectName = boardD
	}
	if len(binding.RelayEndpoints) == 0 {
		binding.RelayEndpoints = eps
	}
	return rdconfig.SaveBoardBinding(dir, binding)
}

// importFollowedBoard pulls the board's FULL history into dir's authoritative log:
// it fetches the board's card+status+grant events from the relays (board-scoped
// filter), unions them with the discovery snapshot, admits only schnorr-verified
// events authored by a key in the board owner's derived read-trust set (so a
// hostile relay's foreign-key events are dropped fail-closed), and AppendUniques
// them. It then runs a best-effort board-level backfill (PublishBoard) so a
// freshly-bound box re-seeds the relays with exactly this board's events.
func importFollowedBoard(ctx context.Context, dir, coord, owner, boardD string, snapshot []*nostr.Event, relays []string) error {
	boardEvents, ferr := followFetch(ctx, relays, rdSync.BoardSyncFilter(coord, nil))
	if ferr != nil {
		boardEvents = nil // best-effort: a relay miss must not fail the bind
	}
	all := append(append([]*nostr.Event{}, snapshot...), boardEvents...)
	trusted := rdSync.DeriveReadTrust(all, owner, boardD)

	log := rdSync.NewNostrLog(rdSync.NostrLogPath(dir))
	admit := make([]*nostr.Event, 0, len(all))
	for _, e := range all {
		if e == nil || e.Verify() != nil {
			continue
		}
		if !trusted[e.PubKey] {
			continue
		}
		if !rdSync.EventBelongsToBoard(e, coord) {
			continue
		}
		admit = append(admit, e)
	}
	if _, err := log.AppendUnique(admit); err != nil {
		return fmt.Errorf("importing board history: %w", err)
	}

	// Best-effort board-level backfill (relay convergence). Never fatal.
	pub := &rdSync.Publisher{
		Key:         mustFollowKey(),
		Log:         log,
		WriteRelays: relays,
		PendingPath: nostrPendingPath(dir),
	}
	_, _ = pub.PublishBoard(ctx, coord)
	return nil
}

// publishFollowAlias publishes ONE kind-39302 person-alias for this machine's key
// under handle email, into dir's log + the relays. It reuses the same monotonic
// created_at slot logic as `rd identify` so a re-follow supersedes rather than
// ties the prior alias. Returns the published event id.
func publishFollowAlias(ctx context.Context, dir string, k *nostr.Key, email string, relays []string) (string, error) {
	if dir == "" {
		return "", fmt.Errorf("no board bound — cannot anchor the person-alias")
	}
	log := rdSync.NewNostrLog(rdSync.NostrLogPath(dir))
	spec := identity.AliasSpec{
		Handle:  email,
		Pubkeys: []string{k.PubKeyHex()},
		Emails:  []string{email},
	}
	ev, err := identity.BuildAliasEvent(k, spec, nextAliasCreatedAt(log, email))
	if err != nil {
		return "", err
	}
	pub := &rdSync.Publisher{
		Key:         k,
		Log:         log,
		WriteRelays: relays,
		PendingPath: nostrPendingPath(dir),
	}
	if _, err := pub.PublishEvents(ctx, []*nostr.Event{ev}); err != nil {
		return "", err
	}
	return ev.ID, nil
}

// printFollowReport prints the human summary: one line per bound board (by NAME,
// never a raw coordinate to retype) and the exact grant line for the owner.
func printFollowReport(rep *followReport) {
	verb := "kept existing"
	if rep.MintedKey {
		verb = "minted a new"
	}
	fmt.Printf("followed %d board(s) — %s machine key:\n", len(rep.BoardDirs), verb)
	names := make([]string, 0, len(rep.BoardDirs))
	for d := range rep.BoardDirs {
		names = append(names, d)
	}
	sort.Strings(names)
	for _, d := range names {
		fmt.Printf("  %-20s -> %s\n", d, rep.BoardDirs[d])
	}
	fmt.Println()
	fmt.Println("Run 'rd ready' in any board directory to see its items now.")
	fmt.Println()
	fmt.Println("Send the owner this pubkey — they grant write access to ALL your followed boards at once:")
	fmt.Printf("  rd grant --all-boards %s\n", rep.Pubkey)
}

// --- small helpers -------------------------------------------------------------

// mustFollowKey loads this machine's key (already load-or-created by runFollow's
// keep-or-mint step, so this never mints here).
func mustFollowKey() *nostr.Key {
	k, _ := nostrKey()
	return k
}

func relayEndpointsFrom(relays []string) []rdconfig.RelayEndpoint {
	eps := make([]rdconfig.RelayEndpoint, 0, len(relays))
	for _, u := range relays {
		if u == "" {
			continue
		}
		eps = append(eps, rdconfig.RelayEndpoint{URL: u, Read: true, Write: true})
	}
	return eps
}

func dedupStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func init() {
	followCmd.Flags().String("board", "", "follow only the board with this d identifier (default: all of the owner's boards)")
	followCmd.Flags().String("email", "", "party handle to register for this machine's key (default: "+defaultFollowEmail+")")
	followCmd.Flags().String("root", "", "projects root under which one directory per board is created (default: current directory)")
	followCmd.Flags().Bool("all", false, "skip the confirmation prompt when more than a few boards are discovered (non-interactive/scripted use)")
	followCmd.Flags().Bool("yes", false, "alias for --all")
	rootCmd.AddCommand(followCmd)
}

// --- bech32 npub decode (BIP-173 / NIP-19) ------------------------------------
//
// The repo carries no bech32 dependency, so `rd follow npub1...` decodes it here.
// A NIP-19 npub is bech32 with hrp="npub" whose 5-bit data payload converts to the
// 32-byte x-only pubkey. Standalone + checksum-verified so a mistyped npub is
// rejected rather than silently followed to the wrong owner.

const bech32Charset = "qpzry9x8gf2tvdw0s3jn54khce6mua7l"

func decodeNpub(s string) (string, error) {
	hrp, data, err := bech32Decode(s)
	if err != nil {
		return "", fmt.Errorf("invalid npub: %w", err)
	}
	if hrp != "npub" {
		return "", fmt.Errorf("not an npub (bech32 hrp=%q)", hrp)
	}
	conv, err := convertBits(data, 5, 8, false)
	if err != nil {
		return "", fmt.Errorf("invalid npub payload: %w", err)
	}
	if len(conv) != 32 {
		return "", fmt.Errorf("npub decodes to %d bytes, want a 32-byte pubkey", len(conv))
	}
	b := make([]byte, len(conv))
	for i, v := range conv {
		b[i] = byte(v)
	}
	return hex.EncodeToString(b), nil
}

func bech32Polymod(values []int) int {
	gen := []int{0x3b6a57b2, 0x26508e6d, 0x1ea119fa, 0x3d4233dd, 0x2a1462b3}
	chk := 1
	for _, v := range values {
		b := chk >> 25
		chk = (chk&0x1ffffff)<<5 ^ v
		for i := 0; i < 5; i++ {
			if (b>>uint(i))&1 == 1 {
				chk ^= gen[i]
			}
		}
	}
	return chk
}

func bech32HrpExpand(hrp string) []int {
	out := make([]int, 0, len(hrp)*2+1)
	for _, c := range hrp {
		out = append(out, int(c)>>5)
	}
	out = append(out, 0)
	for _, c := range hrp {
		out = append(out, int(c)&31)
	}
	return out
}

func bech32VerifyChecksum(hrp string, data []int) bool {
	return bech32Polymod(append(bech32HrpExpand(hrp), data...)) == 1
}

func bech32Decode(s string) (string, []int, error) {
	if s != strings.ToLower(s) && s != strings.ToUpper(s) {
		return "", nil, fmt.Errorf("mixed-case string")
	}
	s = strings.ToLower(s)
	pos := strings.LastIndex(s, "1")
	if pos < 1 || pos+7 > len(s) {
		return "", nil, fmt.Errorf("no separator or bad length")
	}
	hrp := s[:pos]
	var data []int
	for _, c := range s[pos+1:] {
		idx := strings.IndexRune(bech32Charset, c)
		if idx < 0 {
			return "", nil, fmt.Errorf("invalid character %q", c)
		}
		data = append(data, idx)
	}
	if !bech32VerifyChecksum(hrp, data) {
		return "", nil, fmt.Errorf("bad checksum")
	}
	return hrp, data[:len(data)-6], nil
}

func convertBits(data []int, fromBits, toBits uint, pad bool) ([]int, error) {
	acc := 0
	bits := uint(0)
	var out []int
	maxv := (1 << toBits) - 1
	for _, value := range data {
		if value < 0 || value>>fromBits != 0 {
			return nil, fmt.Errorf("invalid data range")
		}
		acc = (acc << fromBits) | value
		bits += fromBits
		for bits >= toBits {
			bits -= toBits
			out = append(out, (acc>>bits)&maxv)
		}
	}
	if pad {
		if bits > 0 {
			out = append(out, (acc<<(toBits-bits))&maxv)
		}
	} else if bits >= fromBits || ((acc<<(toBits-bits))&maxv) != 0 {
		return nil, fmt.Errorf("invalid padding")
	}
	return out, nil
}
