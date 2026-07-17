package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/rdconfig"
	"github.com/3dl-dev/ready/pkg/state"
	rdSync "github.com/3dl-dev/ready/pkg/sync"
	"github.com/spf13/cobra"
)

// nostrEnabled reports whether RD_NOSTR forces the nostr publish path on. It is
// redundant on a normal (nostr-native) project, where publishing is always active;
// the env var only forces it for a directory with no pinned board.
func nostrEnabled() bool { return os.Getenv("RD_NOSTR") == "1" }

// autoReconcileTimeout bounds the opportunistic relay dial on the read hot path
// (rd ready/list/show). Short by design: a read must not block on an unreachable
// relay — the local log is authoritative, and reconcile is best-effort. The manual
// `rd sync` still uses the full nostr.DefaultTimeout for a thorough convergence.
const autoReconcileTimeout = 2 * time.Second

// nostrWriteRelays returns the write-relay URLs, honoring an RD_NOSTR_RELAY_URL
// override (single relay — used by the demo to target one relay or a deliberately
// unreachable endpoint for the relay-offline proof). Endpoints otherwise come
// from pkg/rdconfig defaults (never hardcoded here).
func nostrWriteRelays() []string {
	if u := os.Getenv("RD_NOSTR_RELAY_URL"); u != "" {
		return []string{u}
	}
	eps := resolveRelayConfig()
	return relayURLs(eps, func(r rdconfig.RelayEndpoint) bool { return r.Write })
}

func nostrReadRelays() []string {
	if u := os.Getenv("RD_NOSTR_RELAY_URL"); u != "" {
		return []string{u}
	}
	eps := resolveRelayConfig()
	return relayURLs(eps, func(r rdconfig.RelayEndpoint) bool { return r.Read })
}

func relayURLs(eps []rdconfig.RelayEndpoint, want func(rdconfig.RelayEndpoint) bool) []string {
	var out []string
	for _, r := range eps {
		if want(r) {
			out = append(out, r.URL)
		}
	}
	return out
}

// resolveRelayConfig resolves the relay endpoints for the current directory using
// a config CASCADE (nearest declaration wins):
//
//  1. env RD_NOSTR_RELAY_URL — handled by the callers above (highest precedence).
//  2. Walk UP from cwd. The first .ready/config.json that DECLARES a relay policy
//     wins: RelaysLocalOnly=true → local-only (empty, stop — the explicit opt-out
//     that prevents a --local project inheriting an ancestor's relay); non-empty
//     RelayEndpoints → those. A config that declares neither is transparent and the
//     walk continues to its parent.
//  3. The home rd.json ($RD_HOME) RelayEndpoints — the machine-wide default.
//  4. Nothing → local-only.
//
// So a user sets relays once (home rd.json, or an umbrella .ready/config.json) and
// every project under it inherits, while any project overrides with its own
// --relay, and --local opts out without leaking to the inherited relay.
func resolveRelayConfig() []rdconfig.RelayEndpoint {
	dir, err := os.Getwd()
	if err == nil {
		for {
			if eps, localOnly, declared := relayPolicyAt(dir); declared {
				if localOnly {
					return nil // explicit opt-out — stop, no inheritance
				}
				return eps // declared here — wins
			}
			// declares neither: transparent, keep walking up
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	// Home fallback: the machine-wide default in $RD_HOME/rd.json.
	if cfg, herr := rdconfig.Load(RDHome()); herr == nil && cfg != nil {
		return cfg.RelayEndpoints
	}
	return nil
}

// relayPolicyAt reports the relay policy DECLARED at a single cascade level (dir).
// declared=false means the level is transparent (neither the machine-local
// config.json nor the committed board.json declares a policy) and the caller keeps
// walking up. Within a level the machine-local config.json is consulted FIRST so a
// local override — and the RelaysLocalOnly opt-out — wins over the committed
// board.json; the committed .ready/board.json (ready-f12) then supplies the relays a
// fresh clone carries, so a repo with only board.json still resolves its relays.
func relayPolicyAt(dir string) (eps []rdconfig.RelayEndpoint, localOnly, declared bool) {
	cfgPath := rdconfig.SyncConfigPath(dir)
	if fileExists(cfgPath) {
		if sc, lerr := rdconfig.LoadSyncConfig(dir); lerr != nil {
			fmt.Fprintf(os.Stderr, "warning: could not read %s (%v) — skipping this level of relay config\n", cfgPath, lerr)
		} else if sc.RelaysLocalOnly {
			return nil, true, true
		} else if len(sc.RelayEndpoints) > 0 {
			return sc.RelayEndpoints, false, true
		}
	}
	bindingPath := rdconfig.BoardBindingPath(dir)
	if fileExists(bindingPath) {
		if b, berr := rdconfig.LoadBoardBinding(dir); berr != nil {
			fmt.Fprintf(os.Stderr, "warning: could not read %s (%v) — skipping this level of relay config\n", bindingPath, berr)
		} else if len(b.RelayEndpoints) > 0 {
			return b.RelayEndpoints, false, true
		}
	}
	return nil, false, false
}

// rdActor resolves the DURABLE actor id this process signs as (BP-4). $RD_ACTOR
// selects it; unset (or empty) defaults to nostr.OwnerActor ("owner"), which maps
// to the LEGACY single-key path — so an existing single-key install signs exactly
// as before, zero migration. A named agent (e.g. "agent:pm") selects a DISTINCT
// on-disk key at $RD_HOME/keys/<sanitized>.json with a DISTINCT pubkey, so an owner
// and an agent on the SAME host sign with different keys and are attributed
// distinctly. Keys are per durable actor, NEVER per-process (design §2).
func rdActor() string {
	if a := os.Getenv("RD_ACTOR"); a != "" {
		return a
	}
	return nostr.OwnerActor
}

// nostrKey loads (or first-run creates) the portfolio secp256k1 signing key for
// the CURRENT actor ($RD_ACTOR, default "owner") from the rd home ($RD_HOME /
// RDHome()). A first-run OWNER key is generated fresh; a NAMED agent's key is also
// generated fresh and is INERT until granted+allowlisted (BP-5). Emits a loud
// warning if the loaded identity is self-inconsistent.
func nostrKey() (*nostr.Key, error) {
	rdHome := RDHome()
	keyPath, err := nostr.ActorKeyPath(rdHome, rdActor())
	if err != nil {
		return nil, fmt.Errorf("nostr: resolve actor key path: %w", err)
	}
	k, err := nostr.LoadOrCreatePortfolioKey(keyPath, rdHome)
	if err != nil {
		return nil, err
	}
	warnIfIdentityInconsistent(keyPath, k)
	return k, nil
}

// nostrTrustSet builds the read-side web-of-trust allowlist (ready-d53): the self
// pubkey (always trusted) unioned with the admitted TrustedPubkeys from the global
// rd config AND — GAP-1 (ready-7c1) — the grant-derived membership for the pinned
// board (every owner-rooted, cap-valid grantee in the local log). This is "one signed
// source feeds everything": an owner-GRANTED contributor absent from rd.json is now
// admitted at ALL FOUR read seams (reconcile ingestion, negentropy download, degrade-
// floor merge, and projection) by its owner-signed grant alone, so ingestion and
// projection agree.
//
// The bootstrap is non-circular: DeriveReadTrust always includes the board author
// (the pin names the owner pubkey), so owner-signed grants are always admitted, and
// each admitted grant expands the set (re-reconcile converges). Config.TrustedPubkeys
// is RETAINED as the bootstrap/fallback union so existing installs never break, and
// self is always trusted. Fail-closed: a key with neither a grant nor a config/self
// entry is still dropped. A missing/unreadable config or log degrades to
// config+self trust (a permissive relay still cannot inject state from an ungranted
// foreign key).
func nostrTrustSet(dir, selfPubkey string) map[string]bool {
	set := loadRDConfig().TrustSet(selfPubkey)
	pin := nostrPinnedBoard(dir)
	if pin == "" {
		return set // unpinned install — bootstrap trust only (pre-GAP-1 behaviour).
	}
	owner, boardD, ok := rdSync.ParseBoardCoord(pin)
	if !ok {
		return set
	}
	events, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
	if err != nil {
		return set // fail-closed: keep the strict bootstrap set on a log read error.
	}
	for pk := range rdSync.DeriveReadTrust(events, owner, boardD) {
		set[pk] = true
	}
	return set
}

// nostrNextCreatedAt returns a strictly-monotonic-PER-CAUSAL-CHAIN event timestamp
// in unix SECONDS: max(now, newestSecondForThisScope+1). scope is the target write's
// DriftScope (rdSync.ItemDriftScope / GrantDriftScope). The nostr projection resolves
// latest-wins at second granularity, tie-breaking identical seconds by event id
// (ready-f92, for cross-machine convergence). That tie-break is order-INDEPENDENT, so
// two mutations issued within the same wall-clock second (e.g. a scripted
// `rd create && rd claim`, common on the nostr-native default path) would otherwise
// resolve in content-hash order, not intent order — a create card could win over the
// later claim card, leaving the item stuck at inbox. Stamping each successive write at
// least one second AFTER the newest event ALREADY IN THIS SCOPE restores intent order
// for that item's (or that grantee-grant's) sequential writes.
//
// ready-be1 — SCOPED, not log-wide: the newest is taken over events in the SAME causal
// chain only, never the whole log. Latest-wins orders each item's card/status chain and
// each role-grant slot independently, so intent ordering only needs to hold within a
// chain. The old log-wide max let an unrelated burst (rd engage over N items, a grant
// burst) inflate the created_at of the NEXT write to ANY item/grantee by one second per
// burst event — drifting a fresh card/grant arbitrarily into the future, where it beat a
// genuinely-later cross-machine edit/REVOKE (silent lost update / ignored revoke,
// violating ready-f92 convergence). Scoping bounds any single card/grant's future-drift
// to the count of same-second writes to THAT chain (a couple of seconds), so an honest
// later real-time edit from another machine wins. Convergence is unchanged: the ordering
// key (created_at, id) is untouched; genuinely concurrent cross-machine events still
// tie-break by id. Degrades to time.Now on a log read error (the pre-existing behaviour).
func nostrNextCreatedAt(log *rdSync.NostrLog, scope string) int64 {
	now := time.Now().Unix()
	events, err := log.ReadAll()
	if err != nil {
		return now
	}
	var max int64
	for _, e := range events {
		if rdSync.DriftScope(e) != scope {
			continue // different causal chain — its drift must not poison this write
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

// nostrPublisher builds a Publisher rooted at the current project. Returns
// (nil,false,nil) when there is no project dir (nothing to publish into).
func nostrPublisher() (*rdSync.Publisher, bool, error) {
	dir, ok := readyProjectDir()
	if !ok {
		return nil, false, nil
	}
	k, err := nostrKey()
	if err != nil {
		return nil, false, err
	}
	return &rdSync.Publisher{
		Key:         k,
		Log:         rdSync.NewNostrLog(rdSync.NostrLogPath(dir)),
		WriteRelays: nostrWriteRelays(),
		PendingPath: nostrPendingPath(dir),
	}, true, nil
}

func nostrPendingPath(dir string) string {
	return dir + "/.ready/" + rdSync.NostrPendingFile
}

// nostrPinnedBoard returns the pinned authoritative board coordinate for the
// project rooted at dir (BP-3). Resolution reads the COMMITTED binding
// (.ready/board.json, ready-f12) FIRST so a fresh clone / worktree that carries
// only the tracked binding resolves its board with no link/follow step; it falls
// back to the machine-local .ready/config.json's SyncConfig for legacy installs
// that predate the split. Empty when unpinned (the default for existing installs)
// or unreadable — an empty pin disables the projection's board-rejection /
// level-derivation gates, preserving pre-BP-3 behaviour. It is passed to
// ProjectOptions.PinnedBoard so the nostr projection rejects foreign-board cards
// and derives graded operator levels.
func nostrPinnedBoard(dir string) string {
	if b, err := rdconfig.LoadBoardBinding(dir); err == nil && b.Board != "" {
		return b.Board
	}
	cfg, err := rdconfig.LoadSyncConfig(dir)
	if err != nil {
		return ""
	}
	return cfg.Board
}

// boardSpecForProject returns the NIP-100 board (project) spec for the current
// project directory, with the portfolio key as sole maintainer.
func boardSpecForProject(dir, pubkey string) rdSync.BoardSpec {
	name := projectPrefix(dir)
	return rdSync.BoardSpec{BoardD: name, Title: name, Maintainers: []string{pubkey}}
}

// nostrBoardAuthor resolves the OWNER pubkey that authored this project's 30301
// board — the pubkey a card's board-membership "a" coordinate must reference so an
// AGENT-signed card still belongs to the OWNER's board and is accepted by BP-3's
// pin (BP-4 reconciliation). Resolution: the pinned board coordinate in
// .ready/config.json (30301:<owner>:<boardD>) is authoritative when set; otherwise
// fall back to the signer's own pubkey — the owner signing their own board, which
// reproduces the pre-pin behaviour exactly (zero migration for existing installs).
func nostrBoardAuthor(dir, signerPubkey string) (string, error) {
	if pin := nostrPinnedBoard(dir); pin != "" {
		owner, _, ok := rdSync.ParseBoardCoord(pin)
		if !ok {
			// HIGH-2 (fail-open fix): a present-but-unparseable pin must HARD-ERROR,
			// matching resolveBoardAuthorD. Silently falling back to signerPubkey here
			// published items under the WRONG authority (the signer's own key instead
			// of the intended board owner), diverging the item onto a foreign board.
			return "", fmt.Errorf("pinned board coordinate %q is malformed (want 30301:<owner>:<boardD>); "+
				"refusing to publish under the signer's own authority — fix .ready/config.json", pin)
		}
		return owner, nil
	}
	return signerPubkey, nil
}

// warnNostrPublishFailure prints the standard best-effort nostr-publish-failure
// warning to stderr: action names WHAT was already durably committed elsewhere
// (campfire/JSONL, or the local log/campfire for create), so every call site
// reports the identical shape without re-deriving the wording. A relay failure
// here never fails the caller's mutation — this is purely diagnostic.
func warnNostrPublishFailure(action string, err error) {
	fmt.Fprintf(os.Stderr, "warning: nostr publish failed (%s): %v\n", action, err)
}

// closeResolutionToStatus maps an rd close resolution (done/cancelled/failed) to
// the rd status string, mirroring pkg/state's handleWorkClose switch so the
// nostr status-change publish carries the exact same terminal status.
func closeResolutionToStatus(resolution string) string {
	switch resolution {
	case "cancelled":
		return state.StatusCancelled
	case "failed":
		return state.StatusFailed
	default:
		return state.StatusDone
	}
}

// publishItemStatusChangeNostr is the status-change hook (claim / status update /
// close-done / fail / cancel): when nostr is enabled, publish a refreshed card
// (current field state) PLUS a NIP-34 status event carrying the optional
// close/change reason. This is what makes `rd show`'s history replay see every
// transition, with close-with-reason preserved exactly (ready-b5f). Best-effort:
// mirrors publishItemFullCreateNostr — a relay failure never fails the caller's
// mutation, since the campfire/JSONL write already succeeded and the nostr event
// is durable in the local authoritative log regardless of relay reachability.
func publishItemStatusChangeNostr(item *state.Item, reason string) error {
	if !nostrWriteActive() {
		return nil
	}
	pub, ok, err := nostrPublisher()
	if err != nil || !ok {
		return err
	}
	dir, _ := readyProjectDir()
	boardAuthor, err := nostrBoardAuthor(dir, pub.Key.PubKeyHex())
	if err != nil {
		return err
	}
	board := boardSpecForProject(dir, boardAuthor)
	card := rdSync.CardSpecFromItem(item, board.BoardD)
	card.BoardAuthor = boardAuthor // agent-signed card joins the OWNER's pinned board (BP-4)
	// Confidential-by-default (ready-216): seal the card + the status reason.
	if err := setCardEnvelope(dir, pub, boardAuthor, board.BoardD, &card); err != nil {
		return err
	}
	res, err := pub.PublishStatusChange(context.Background(), card, reason, nostrNextCreatedAt(pub.Log, rdSync.ItemDriftScope(item.ID)))
	if err != nil {
		return err
	}
	if debugOutput {
		for _, ev := range res.Events {
			fmt.Fprintf(os.Stderr, "nostr: published kind %d id %s (relay-accepted=%v)\n", ev.Kind, ev.EventID, ev.AnyRelay)
		}
	}
	return nil
}

// publishItemCardEditNostr is the card-only-edit hook (progress notes, title/
// context/priority updates with NO status change): publishes a refreshed card
// with no accompanying status event, proving the hybrid invariant that editing
// the addressable 30302 card does not add — or erase — history (ready-b5f).
func publishItemCardEditNostr(item *state.Item) error {
	if !nostrWriteActive() {
		return nil
	}
	pub, ok, err := nostrPublisher()
	if err != nil || !ok {
		return err
	}
	dir, _ := readyProjectDir()
	boardAuthor, err := nostrBoardAuthor(dir, pub.Key.PubKeyHex())
	if err != nil {
		return err
	}
	board := boardSpecForProject(dir, boardAuthor)
	card := rdSync.CardSpecFromItem(item, board.BoardD)
	card.BoardAuthor = boardAuthor // agent-signed card joins the OWNER's pinned board (BP-4)
	// Confidential-by-default (ready-216): re-seal the edited card's free text.
	if err := setCardEnvelope(dir, pub, boardAuthor, board.BoardD, &card); err != nil {
		return err
	}
	res, err := pub.PublishCardEdit(context.Background(), card, nostrNextCreatedAt(pub.Log, rdSync.ItemDriftScope(item.ID)))
	if err != nil {
		return err
	}
	if debugOutput {
		for _, ev := range res.Events {
			fmt.Fprintf(os.Stderr, "nostr: published kind %d id %s (relay-accepted=%v)\n", ev.Kind, ev.EventID, ev.AnyRelay)
		}
	}
	return nil
}

// strSliceAppendUnique appends v to s only if absent (order-preserving).
func strSliceAppendUnique(s []string, v string) []string {
	for _, x := range s {
		if x == v {
			return s
		}
	}
	return append(s, v)
}

// strSliceRemove returns s with every element equal to any of vals removed.
func strSliceRemove(s []string, vals ...string) []string {
	drop := make(map[string]bool, len(vals))
	for _, v := range vals {
		drop[v] = true
	}
	out := make([]string, 0, len(s))
	for _, x := range s {
		if !drop[x] {
			out = append(out, x)
		}
	}
	return out
}

// nostrReconcileItemIntoLog cache-fills a single item from the read relays into the
// local authoritative log before it is reconstructed, and returns a human-readable
// note describing what was fetched. It is the substrate behind `rd show --reconcile`
// (ready-f58: the unique `rd nostr show --reconcile` capability, migrated onto the
// top-level `rd show`). The local log stays authoritative — the relay fetch only
// adds trust-gated events that were missing locally.
func nostrReconcileItemIntoLog(itemID string) (string, error) {
	dir, ok := readyProjectDir()
	if !ok {
		return "", fmt.Errorf("no .ready project directory found")
	}
	log := rdSync.NewNostrLog(rdSync.NostrLogPath(dir))
	k, err := nostrKey()
	if err != nil {
		return "", err
	}
	r, err := rdSync.ReconcileItem(context.Background(), nostrReadRelays(), log, itemID, nostrTrustSet(dir, k.PubKeyHex()), autoReconcileTimeout)
	if err != nil {
		return "", err
	}
	if debugOutput {
		for _, e := range r.RelayErrors {
			fmt.Fprintf(os.Stderr, "nostr: reconcile relay error: %s\n", e)
		}
	}
	return fmt.Sprintf("reconciled: fetched=%d added=%d relay_errors=%d", r.Fetched, r.Added, len(r.RelayErrors)), nil
}

// nostrPublishCmd re-publishes an existing rd item's CURRENT state (read from the
// campfire/JSONL projection) as board+card+status events. Useful for migrating
// items created before nostr was enabled, and for the demo's status-change path.
//
// --board (ready-866, ready-615 edge #4) switches this command to the BOARD-LEVEL
// primitive: instead of re-materializing one item's current state, it re-publishes
// EVERY event already durable in the local authoritative log for the pinned board
// — verbatim, unmodified — to the write relays. This is the fix for "3dl had 547
// cards locally but ~12 on relays": the live per-mutation publish hooks only ever
// push the event THEY just built, so any item mutated while a relay was
// unreachable, or created before a relay was configured at all, never reaches the
// relay until something explicitly re-sends it. `--board` is that explicit
// re-send, scoped to the whole board in one call, so a fresh box can converge
// from relays alone (no scp of nostr-log.jsonl).
var nostrPublishCmd = &cobra.Command{
	Use:   "publish [item-id]",
	Short: "Publish an existing rd item's current state to the nostr log + relays",
	Long: `Publish an existing rd item's current state to the nostr log + relays.

  rd log publish <item-id>   Re-publish ONE item's current state (board+card+status).
  rd log publish --board     Re-publish EVERY local-log event for the pinned board —
                              the whole append-only history, not just current state —
                              so a fresh box can converge from relays alone.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		boardFlag, _ := cmd.Flags().GetBool("board")
		if boardFlag {
			if len(args) != 0 {
				return fmt.Errorf("--board takes no item-id argument (it publishes every event for the pinned board)")
			}
			return runPublishBoard()
		}
		if len(args) != 1 {
			return fmt.Errorf("publish requires an item-id argument, or --board to publish the whole board")
		}
		itemID := args[0]
		dir, ok := readyProjectDir()
		if !ok {
			return fmt.Errorf("no .ready project directory found")
		}
		// Load current item state from the nostr projection (the resolver every
		// other nostr-native command uses) — NOT the legacy JSONL projection,
		// which does not exist on a nostr-native project (ready-50a).
		item, err := nostrResolveItem(itemID)
		if err != nil {
			return err
		}
		pub, ok, err := nostrPublisher()
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("no project dir for publisher")
		}
		signer := pub.Key.PubKeyHex()
		boardAuthor, err := nostrBoardAuthor(dir, signer)
		if err != nil {
			return err
		}
		board := boardSpecForProject(dir, boardAuthor)
		// Route through the SINGLE shared helper (ready-187). The old inline literal
		// omitted Labels/ETA/Assignee (and would never have carried Level/For/Parent/
		// Due), so `rd nostr publish` clobbered them to empty on the latest-wins card.
		card := rdSync.CardSpecFromItem(item, board.BoardD)
		card.BoardAuthor = boardAuthor // agent-signed card joins the OWNER's pinned board (BP-4)
		var boardArg *rdSync.BoardSpec
		if signer == boardAuthor {
			boardArg = &board // only the owner can author the owner's board
		}
		// Carry the item's already-recorded close/change reason through the manual
		// republish path too (ready-da7), matching publishItemStatusChangeNostr's
		// explicit-reason live hook (ready-2cf/b5f) — this command previously always
		// published its status event with an empty reason, dropping close-with-reason
		// on ANY re-publish after the initial live write.
		reason := lastStatusReason(item)
		res, err := pub.PublishItemWithReason(context.Background(), boardArg, card, reason, time.Now().Unix())
		if err != nil {
			return err
		}
		if jsonOutput {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(res)
		}
		for _, ev := range res.Events {
			fmt.Printf("published kind %d id %s relay-accepted=%v\n", ev.Kind, ev.EventID, ev.AnyRelay)
		}
		if res.Buffered {
			fmt.Println("(some events reached no relay; buffered to nostr-pending.jsonl — durable in local log)")
		}
		if res.Rejected {
			fmt.Fprintln(os.Stderr, "WARNING: some events were permanently rejected by a relay (malformed/disallowed) and dead-lettered to nostr-rejected.jsonl — they will NOT be retried; inspect and fix.")
		}
		return nil
	},
}

// lastStatusReason returns the reason/note associated with the item's CURRENT
// status — the most recent history entry whose to_status matches item.Status —
// or "" when there is none (e.g. a freshly-created item with no history yet, or
// a status reached with no reason given). ready-da7: `rd nostr publish`
// re-derives the reason from history (it has no reason argument of its own,
// unlike the live claim/close/cancel hooks) so close-with-reason survives a
// manual republish.
func lastStatusReason(item *state.Item) string {
	for i := len(item.History) - 1; i >= 0; i-- {
		if item.History[i].ToStatus == item.Status {
			return item.History[i].Note
		}
	}
	return ""
}

// runPublishBoard is `rd log publish --board` (ready-866): it re-publishes EVERY
// event already durable in the local authoritative log for the pinned board to
// the write relays, via the low-level Publisher.PublishBoard primitive. Unlike
// the per-item publish path above, this does not re-derive or re-sign anything —
// it sends exactly what is already in nostr-log.jsonl, so a fresh box that
// reconciles against the relays afterward converges to the SAME state this
// machine's local log holds, with no scp required.
//
// Board scope: the PINNED board coordinate (.ready/config.json's SyncConfig.Board)
// when set; an unpinned project falls back to publishing the WHOLE local log
// unscoped, mirroring nostrReconcileBoardIntoLog's ReconcileBoard fallback for the
// same unpinned case (ready-7ec) — an unpinned install has no board scope to
// filter by, so "the board" is trivially "everything this identity has written".
func runPublishBoard() error {
	dir, ok := readyProjectDir()
	if !ok {
		return fmt.Errorf("no .ready project directory found")
	}
	pub, ok, err := nostrPublisher()
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("no project dir for publisher")
	}
	boardCoord := nostrPinnedBoard(dir)
	res, err := pub.PublishBoard(context.Background(), boardCoord)
	if err != nil {
		return err
	}
	if jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(res)
	}
	fmt.Printf("publish --board: %d event(s) for board %q\n", len(res.Events), boardCoord)
	for _, ev := range res.Events {
		fmt.Printf("published kind %d id %s relay-accepted=%v\n", ev.Kind, ev.EventID, ev.AnyRelay)
	}
	if res.Buffered {
		fmt.Println("(some events reached no relay; buffered to nostr-pending.jsonl — durable in local log)")
	}
	if res.Rejected {
		fmt.Fprintln(os.Stderr, "WARNING: some events were permanently rejected by a relay (malformed/disallowed) and dead-lettered to nostr-rejected.jsonl — they will NOT be retried; inspect and fix.")
	}
	return nil
}

// nostrReconcileBoardIntoLog cache-fills the WHOLE pinned board from the read relays
// into the local authoritative log before readiness is computed. It is the substrate
// behind `rd ready --reconcile` (ready-f58: the `rd nostr ready --reconcile`
// capability, migrated onto the top-level `rd ready`). ready-7ec: the reconcile is
// scoped to the PINNED board when one is set, so it pulls only THIS project's board
// rather than the relay's entire portfolio; unpinned installs fall back to the
// unscoped ReconcileAll behaviour.
func nostrReconcileBoardIntoLog() error {
	dir, ok := readyProjectDir()
	if !ok {
		return fmt.Errorf("no .ready project directory found")
	}
	k, err := nostrKey()
	if err != nil {
		return err
	}
	log := rdSync.NewNostrLog(rdSync.NostrLogPath(dir))
	r, err := rdSync.ReconcileBoard(context.Background(), nostrReadRelays(), log, nostrPinnedBoard(dir), nostrTrustSet(dir, k.PubKeyHex()), autoReconcileTimeout)
	if err != nil {
		return err
	}
	if debugOutput {
		fmt.Fprintf(os.Stderr, "nostr: reconcile-all fetched=%d added=%d relay_errors=%d\n", r.Fetched, r.Added, len(r.RelayErrors))
	}
	return nil
}

// autoReconcileBoardBestEffort pulls the pinned board's latest events from the
// read relays into the local log before a read, so `rd ready`/`rd list` reflect
// other machines' updates with no manual `rd sync`. Zero-cost no-op when the
// project is local-only (no read relays configured). Best-effort: a relay error
// never blocks the read — the local signed log is authoritative. Skipped when
// offline is true (the read-local escape hatch).
func autoReconcileBoardBestEffort(offline bool) {
	if offline || len(nostrReadRelays()) == 0 {
		return
	}
	_ = nostrReconcileBoardIntoLog()
}

// autoReconcileItemBestEffort is the single-item analogue for `rd show` — same
// no-op-when-local-only and never-fail-the-read contract.
func autoReconcileItemBestEffort(itemID string, offline bool) {
	if offline || len(nostrReadRelays()) == 0 {
		return
	}
	_, _ = nostrReconcileItemIntoLog(itemID)
}


// nostrPutCmd creates-or-updates an rd item directly on the nostr backend: it
// materializes the item as board+card+status events, appends them to the local
// authoritative log (always durable), and publishes to the write relays
// (best-effort; buffered offline). This is the "mutate" primitive the two-machine
// sync demo drives (ready-797).
var nostrPutCmd = &cobra.Command{
	Use:   "put <item-id>",
	Short: "Create/update an rd item directly on the nostr backend (log + relays)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		itemID := args[0]
		title, _ := cmd.Flags().GetString("title")
		status, _ := cmd.Flags().GetString("status")
		priority, _ := cmd.Flags().GetString("priority")
		itemType, _ := cmd.Flags().GetString("type")
		note, _ := cmd.Flags().GetString("note")
		context0, _ := cmd.Flags().GetString("context")

		pub, ok, err := nostrPublisher()
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("no .ready project directory found")
		}
		dir, _ := readyProjectDir()
		board := boardSpecForProject(dir, pub.Key.PubKeyHex())
		if status == "" {
			status = state.StatusActive
		}
		card := rdSync.CardSpec{
			ItemID:   itemID,
			Title:    title,
			Status:   status,
			Priority: priority,
			Type:     itemType,
			Context:  context0,
			BoardD:   board.BoardD,
		}
		var res rdSync.PublishResult
		if note != "" {
			// A status change with a close/change reason (rd close-with-reason).
			res, err = pub.PublishStatusChange(context.Background(), card, note, time.Now().Unix())
		} else {
			res, err = pub.PublishItem(context.Background(), &board, card, time.Now().Unix())
		}
		if err != nil {
			return err
		}
		if jsonOutput {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(res)
		}
		for _, ev := range res.Events {
			fmt.Printf("put kind %d id %s relay-accepted=%v\n", ev.Kind, ev.EventID, ev.AnyRelay)
		}
		if res.Buffered {
			fmt.Println("(some events reached no relay; buffered to nostr-pending.jsonl — durable in local log)")
		}
		if res.Rejected {
			fmt.Fprintln(os.Stderr, "WARNING: some events were permanently rejected by a relay (malformed/disallowed) and dead-lettered to nostr-rejected.jsonl — they will NOT be retried; inspect and fix.")
		}
		return nil
	},
}

// nostrSyncCmd reconciles the local authoritative log against the relays via
// NIP-77 negentropy and performs the resulting download+upload so two machines
// converge on identical item state, transferring only the DIFFERENCE (measured).
var nostrSyncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Negentropy-sync the local nostr log with the relays (two-machine convergence)",
	RunE: func(cmd *cobra.Command, args []string) error {
		dir, ok := readyProjectDir()
		if !ok {
			return fmt.Errorf("no .ready project directory found")
		}
		k, err := nostrKey()
		if err != nil {
			return err
		}
		log := rdSync.NewNostrLog(rdSync.NostrLogPath(dir))
		// ready-7ec: when this project has a PINNED board (SyncConfig.Board), scope
		// the negentropy filter to that board's "a" coordinate — cards already carry
		// it, and status events now do too (BuildStatusEventWithIssueRoot /
		// BuildHistoricalStatusEventWithBoard, additive) — instead of pulling this
		// identity's ENTIRE portfolio (every project/board it has ever authored to,
		// observed at ~9600+ events) on every sync. The board filter matches on the
		// "a" tag regardless of signer, so it also picks up OTHER actors' (agents')
		// events on the same board. Unpinned installs (the pre-ready-7ec default)
		// fall back to the original author-scoped, unbounded-by-board filter —
		// unchanged behaviour, zero migration required.
		var filter map[string]any
		if pin := nostrPinnedBoard(dir); pin != "" {
			filter = rdSync.BoardSyncFilter(pin, nil)
		} else {
			filter = rdSync.BoardSyncFilter("", []string{k.PubKeyHex()})
		}
		relays := nostrWriteRelays()

		ctx := context.Background()
		// ready-b57: gate the negentropy download with the web-of-trust allowlist so a
		// hostile relay cannot inject a validly-signed foreign event into the log.
		trusted := nostrTrustSet(dir, k.PubKeyHex())
		results, errs := rdSync.NegentropySyncMany(ctx, relays, log, filter, trusted, nostr.DefaultTimeout)

		if jsonOutput {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(map[string]any{"results": results, "relay_errors": errs})
		}
		for _, r := range results {
			fmt.Printf("sync %s: local_before=%d need=%d have=%d downloaded=%d uploaded=%d "+
				"neg_bytes(sent=%d recv=%d rounds=%d) event_bytes(down=%d up=%d)\n",
				r.Relay, r.LocalBefore, r.Need, r.Have, r.Downloaded, r.Uploaded,
				r.BytesSent, r.BytesReceived, r.RoundTrips, r.EventBytesDownloaded, r.EventBytesUploaded)
		}
		for _, e := range errs {
			fmt.Fprintf(os.Stderr, "relay error (non-fatal; local log authoritative): %s\n", e)
		}
		return nil
	},
}

// nostrFlushCmd drains the offline pending buffer (nostr-pending.jsonl) to the
// relays on reconnect. Re-publish is idempotent by event id (relays dedupe).
var nostrFlushCmd = &cobra.Command{
	Use:   "flush",
	Short: "Publish buffered offline nostr events to the relays (idempotent by event id)",
	RunE: func(cmd *cobra.Command, args []string) error {
		dir, ok := readyProjectDir()
		if !ok {
			return fmt.Errorf("no .ready project directory found")
		}
		res, err := rdSync.FlushNostrPending(context.Background(), nostrPendingPath(dir), nostrWriteRelays(), nostr.DefaultTimeout)
		if err != nil {
			return err
		}
		if jsonOutput {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(res)
		}
		fmt.Printf("flush: total=%d flushed=%d remaining=%d rejected=%d relay_errors=%d write_errors=%d\n",
			res.Total, res.Flushed, res.Remaining, res.Rejected, len(res.RelayErrors), len(res.WriteErrors))
		if res.Rejected > 0 {
			fmt.Printf("  %d event(s) permanently rejected -> dead-lettered to %s (NOT retried; inspect and fix)\n",
				res.Rejected, rdSync.NostrRejectedFile)
		}
		if len(res.WriteErrors) > 0 {
			fmt.Fprintf(os.Stderr, "  %d local dead-letter WRITE error(s) (disk/permissions, not relay) — events kept in the retry buffer:\n", len(res.WriteErrors))
			for _, we := range res.WriteErrors {
				fmt.Fprintf(os.Stderr, "    %s\n", we)
			}
		}
		return nil
	},
}

// nostrMergeCmd merges another machine's git-committed nostr log (JSONL) into the
// local log. This is the DEGRADE FLOOR: with every relay unreachable, two machines
// still converge by exchanging their committed nostr-log.jsonl and merging
// (idempotent by event id, verify-gated). No relay required.
var nostrMergeCmd = &cobra.Command{
	Use:   "merge-log <other-log.jsonl>",
	Short: "Merge another machine's committed nostr log into the local log (relay-free degrade floor)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		dir, ok := readyProjectDir()
		if !ok {
			return fmt.Errorf("no .ready project directory found")
		}
		log := rdSync.NewNostrLog(rdSync.NostrLogPath(dir))
		// ready-b57: gate the git-JSONL degrade-floor merge with the web-of-trust
		// allowlist. Another machine's committed log is untrusted input; only events
		// from admitted authors (self + TrustedPubkeys) enter the local authoritative
		// log. An admitted machine's log still merges in full.
		k, err := nostrKey()
		if err != nil {
			return err
		}
		added, err := log.MergeFrom(args[0], nostrTrustSet(dir, k.PubKeyHex()))
		if err != nil {
			return err
		}
		if jsonOutput {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(map[string]any{"merged": added})
		}
		fmt.Printf("merge-log: merged %d new event(s) from %s\n", added, args[0])
		return nil
	},
}

// nostrReadEnabled reports whether RD_NOSTR_READ forces the nostr-projection read
// path on. It is redundant on a normal project (reads already resolve from the
// nostr projection); the env var only matters for a directory with no pinned
// board, where it forces the projection read anyway.
func nostrReadEnabled() bool { return os.Getenv("RD_NOSTR_READ") == "1" }

// nostrProjectAllItems reads the local authoritative nostr log and returns the
// projected item set (both as a slice and by id): a pure replay of the
// signed-event log through the same web-of-trust gate the reconcile path uses.
func nostrProjectAllItems() ([]*state.Item, map[string]*state.Item, error) {
	dir, ok := readyProjectDir()
	if !ok {
		return nil, nil, fmt.Errorf("no .ready project directory found")
	}
	k, err := nostrKey()
	if err != nil {
		return nil, nil, err
	}
	log := rdSync.NewNostrLog(rdSync.NostrLogPath(dir))
	events, err := log.ReadAll()
	if err != nil {
		return nil, nil, err
	}
	trusted := nostrTrustSet(dir, k.PubKeyHex())
	// Confidential boards (ready-216): derive this reader's per-board key material
	// from the log (recoverable via the identity key from owner/member grants). A
	// granted member decrypts free text; a non-member sees placeholders; the fold
	// gate quarantines plaintext cards. Nil/empty on a plaintext board → no-op.
	keyring := boardReadKeyring(dir, k, events)
	// ready-b57: status-authority is board-derived (author OR board maintainer),
	// NOT the whole trust set. Read-trust stays the full set; Maintainers left nil.
	byID := rdSync.ProjectItems(events, rdSync.ProjectOptions{
		Trusted:         trusted,
		PinnedBoard:     nostrPinnedBoard(dir),
		Decryptor:       keyring,
		EncryptedBoards: keyring,
	})
	items := make([]*state.Item, 0, len(byID))
	for _, it := range byID {
		items = append(items, it)
	}
	return items, byID, nil
}

// nostrDualReadAll returns (items, true, err) when dual-read is enabled and the
// items were projected from the nostr log; (nil,false,nil) when dual-read is off
// so the caller falls back to the campfire/JSONL path. Additive: no behaviour
// change unless RD_NOSTR_READ=1.
func nostrDualReadAll() ([]*state.Item, bool, error) {
	if !nostrReadActive() {
		return nil, false, nil
	}
	items, _, err := nostrProjectAllItems()
	return items, true, err
}

// nostrDualReadByID resolves one item from the nostr projection when dual-read is
// enabled. Returns (item, true, err) on a hit path (err set if not found);
// (nil,false,nil) when dual-read is off.
func nostrDualReadByID(itemID string) (*state.Item, bool, error) {
	if !nostrReadActive() {
		return nil, false, nil
	}
	_, byID, err := nostrProjectAllItems()
	if err != nil {
		return nil, true, err
	}
	it, ok := byID[itemID]
	if !ok {
		return nil, true, fmt.Errorf("item %q not found in nostr projection", itemID)
	}
	return it, true, nil
}


func init() {
	nostrPublishCmd.Flags().Bool("board", false, "publish EVERY local-log event for the pinned board (fresh-box relay convergence, ready-866) instead of one item's current state")

	nostrPutCmd.Flags().String("title", "", "item title")
	nostrPutCmd.Flags().String("status", "", "rd status (default active)")
	nostrPutCmd.Flags().String("priority", "", "priority (p0..p3)")
	nostrPutCmd.Flags().String("type", "task", "item type")
	nostrPutCmd.Flags().String("context", "", "item description/context (card content)")
	nostrPutCmd.Flags().String("note", "", "status-change reason (rd close-with-reason); publishes a status change instead of a fresh card")

	// Local-log operations live under `rd log`; the relay offline-buffer flush
	// lives under `rd relay`.
	logCmd.AddCommand(nostrPublishCmd)
	logCmd.AddCommand(nostrPutCmd)
	logCmd.AddCommand(nostrMergeCmd)
	// nostrSyncCmd is NOT registered: it is the reused substrate behind the
	// top-level `rd sync` (cmd/rd/sync.go).
	relayCmd.AddCommand(nostrFlushCmd)
}
