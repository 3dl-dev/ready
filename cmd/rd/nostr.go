package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/campfire-net/ready/pkg/nostr"
	"github.com/campfire-net/ready/pkg/rdconfig"
	"github.com/campfire-net/ready/pkg/state"
	rdSync "github.com/campfire-net/ready/pkg/sync"
	"github.com/campfire-net/ready/pkg/views"
	"github.com/spf13/cobra"
)

// nostrEnabled reports whether the rd->nostr publish path is active. It is gated
// off by default so the campfire baseline (and its tests) are unaffected; the
// ground-source demo turns it on with RD_NOSTR=1.
func nostrEnabled() bool { return os.Getenv("RD_NOSTR") == "1" }

// nostrWriteRelays returns the write-relay URLs, honoring an RD_NOSTR_RELAY_URL
// override (single relay — used by the demo to target one relay or a deliberately
// unreachable endpoint for the relay-offline proof). Endpoints otherwise come
// from pkg/rdconfig defaults (never hardcoded here).
func nostrWriteRelays() []string {
	if u := os.Getenv("RD_NOSTR_RELAY_URL"); u != "" {
		return []string{u}
	}
	var cfg rdconfig.Config
	return cfg.WriteRelayURLs()
}

func nostrReadRelays() []string {
	if u := os.Getenv("RD_NOSTR_RELAY_URL"); u != "" {
		return []string{u}
	}
	var cfg rdconfig.Config
	return cfg.ReadRelayURLs()
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
// RDHome()), with NO dependency on campfire's ".cf". On first run it
// identity-preservingly COPIES a legacy .cf key forward into the OWNER slot (never
// regenerates — see migrateRDHomeIfNeeded); a NAMED agent's key is generated fresh
// and is INERT until granted+allowlisted (BP-5). Emits a loud warning if the loaded
// identity is self-inconsistent.
func nostrKey() (*nostr.Key, error) {
	rdHome := RDHome()
	// The migration only ever touches the OWNER slot (legacy .cf -> nostr-identity.json);
	// running it regardless of actor just ensures the owner key exists and is harmless
	// for a named-agent invocation (which loads a different file).
	if err := migrateRDHomeIfNeeded(rdHome); err != nil {
		return nil, fmt.Errorf("nostr identity migration: %w", err)
	}
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
// project rooted at dir (BP-3), read from .ready/config.json's SyncConfig. Empty
// when unpinned (the default for existing installs) or unreadable — an empty pin
// disables the projection's board-rejection / level-derivation gates, preserving
// pre-BP-3 behaviour. It is passed to ProjectOptions.PinnedBoard so the nostr
// projection rejects foreign-board cards and derives graded operator levels.
func nostrPinnedBoard(dir string) string {
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

// publishItemCreateNostr is the create-time hook: when nostr is enabled it
// publishes the board + card + status events for a freshly created item and
// appends them to the local authoritative log. It is best-effort — a relay
// failure never fails `rd create` (the event is durable in the log; the campfire
// / JSONL write already succeeded). Returns nil when nostr is disabled.
func publishItemCreateNostr(itemID, title, itemType, priority, status, itemContext, forParty string) error {
	if !nostrWriteActive() {
		return nil
	}
	pub, ok, err := nostrPublisher()
	if err != nil || !ok {
		return err
	}
	dir, _ := readyProjectDir()
	signer := pub.Key.PubKeyHex()
	boardAuthor, err := nostrBoardAuthor(dir, signer)
	if err != nil {
		return err
	}
	board := boardSpecForProject(dir, boardAuthor)
	if status == "" {
		status = state.StatusInbox
	}
	card := rdSync.CardSpec{
		ItemID:   itemID,
		Title:    title,
		Status:   status,
		Priority: priority,
		Type:     itemType,
		Context:  itemContext,
		BoardD:   board.BoardD,
		// Board MEMBERSHIP points at the OWNER's board coordinate (BP-4), so an
		// agent-signed card is accepted by the owner's pinned board. Owner-signed
		// cards resolve boardAuthor==signer, unchanged.
		BoardAuthor: boardAuthor,
		// Carry the assignment scope at create time (ready-187) — forParty is the
		// only extra field available here. Other card-only fields (labels/eta/due/
		// level/parent) are materialized by the first full-item republish through
		// rdSync.CardSpecFromItem (any subsequent mutation), which supersedes this
		// create card latest-wins.
		For: forParty,
	}
	// Only the board AUTHOR (owner) can sign the owner's 30301 board. An agent must
	// not fork its OWN board (a parallel-board self-escalation BP-3 pins against), so
	// skip the board event when signing as a non-owner actor — its card still joins
	// the owner's board via BoardAuthor above.
	var boardArg *rdSync.BoardSpec
	if signer == boardAuthor {
		boardArg = &board
	}
	ctx := context.Background()
	res, err := pub.PublishItem(ctx, boardArg, card, nostrNextCreatedAt(pub.Log, rdSync.ItemDriftScope(itemID)))
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
// mirrors publishItemCreateNostr — a relay failure never fails the caller's
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

// publishImplicitUnblockNostr mirrors pkg/state's implicit-unblock-on-close
// (handleWorkClose): when an item reaches a terminal status, campfire removes the
// dependency edge from EVERY item that item was blocking, so those items no longer
// list it in blocked_by. The nostr card of a blocked item still carries the "i"
// dep tag, so without this the projection would keep the stale edge and diverge.
// For each item the just-closed item was blocking (blockedIDs, captured from its
// pre-close Blocks list), re-derive the now-current item from campfire (the edge
// is already gone there) and re-publish its card so the nostr projection drops the
// edge too — deps parity across every close path (close/done/fail/cancel/complete
// and cascade). Best-effort; nostr-gated; a relay failure never fails the close.
func publishImplicitUnblockNostr(blockedIDs []string) {
	if !nostrEnabled() || len(blockedIDs) == 0 {
		return
	}
	for _, id := range blockedIDs {
		it, err := byIDFromJSONLOrStore(id)
		if err != nil {
			continue
		}
		if nostrErr := publishItemCardEditNostr(it); nostrErr != nil {
			warnNostrPublishFailure(fmt.Sprintf("implicit-unblock %s; campfire durable", id), nostrErr)
		}
	}
}

var nostrCmd = &cobra.Command{
	Use:   "nostr",
	Short: "rd<->nostr wire-mapping operations (ready-a13)",
	Long: `Operate on the nostr projection of rd work items.

The local append-only signed-event log (.ready/nostr-log.jsonl) is the source of
truth; relays are replaceable caches. 'rd nostr show' reconstructs an item's
CURRENT state by replaying the local log, optionally cache-filling from relays
first.`,
}

var nostrShowCmd = &cobra.Command{
	Use:   "show <item-id>",
	Short: "Reconstruct an item's current state from the local nostr log",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		itemID := args[0]
		reconcile, _ := cmd.Flags().GetBool("reconcile")

		dir, ok := readyProjectDir()
		if !ok {
			return fmt.Errorf("no .ready project directory found")
		}
		log := rdSync.NewNostrLog(rdSync.NostrLogPath(dir))

		var reconcileNote string
		if reconcile {
			k, err := nostrKey()
			if err != nil {
				return err
			}
			ctx := context.Background()
			r, err := rdSync.ReconcileItem(ctx, nostrReadRelays(), log, itemID, nostrTrustSet(dir, k.PubKeyHex()), nostr.DefaultTimeout)
			if err != nil {
				return err
			}
			reconcileNote = fmt.Sprintf("reconciled: fetched=%d added=%d relay_errors=%d", r.Fetched, r.Added, len(r.RelayErrors))
			if debugOutput {
				for _, e := range r.RelayErrors {
					fmt.Fprintf(os.Stderr, "nostr: reconcile relay error: %s\n", e)
				}
			}
		}

		events, err := log.ReadAll()
		if err != nil {
			return err
		}
		k, err := nostrKey()
		if err != nil {
			return err
		}
		trusted := nostrTrustSet(dir, k.PubKeyHex())
		// Status-authority is derived from the board's declared maintainers inside
		// ProjectItems (ready-b57) — NOT the whole trust set. Read-trust stays the
		// full set (Trusted); Maintainers is left nil so only item authors and board
		// maintainers can author authoritative status.
		items := rdSync.ProjectItems(events, rdSync.ProjectOptions{
			Trusted:     trusted,
			PinnedBoard: nostrPinnedBoard(dir),
		})
		item, found := items[itemID]
		if !found {
			return fmt.Errorf("item %q not found in local nostr log (events=%d)%s", itemID, len(events),
				func() string {
					if reconcileNote != "" {
						return "; " + reconcileNote
					}
					return ""
				}())
		}

		if jsonOutput {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(item)
		}
		fmt.Printf("id:       %s\n", item.ID)
		fmt.Printf("title:    %s\n", item.Title)
		fmt.Printf("status:   %s\n", item.Status)
		fmt.Printf("priority: %s\n", item.Priority)
		fmt.Printf("type:     %s\n", item.Type)
		if item.By != "" {
			fmt.Printf("assignee: %s\n", item.By)
		}
		if len(item.History) > 0 {
			// Full audit-trail replay (ready-b5f): every authoritative status
			// event in the append-only log, NOT just the latest-wins card. Editing
			// the card never erases these entries — they are derived exclusively
			// from the NIP-34 status-event chain.
			fmt.Printf("\nhistory:\n")
			for _, h := range item.History {
				note := ""
				if h.Note != "" {
					note = " — " + h.Note
				}
				fmt.Printf("  [%s] %s → %s by %s%s\n", h.Timestamp, h.FromStatus, h.ToStatus, h.ChangedBy, note)
			}
		}
		if reconcileNote != "" {
			fmt.Printf("(%s)\n", reconcileNote)
		}
		return nil
	},
}

// nostrPublishCmd re-publishes an existing rd item's CURRENT state (read from the
// campfire/JSONL projection) as board+card+status events. Useful for migrating
// items created before nostr was enabled, and for the demo's status-change path.
var nostrPublishCmd = &cobra.Command{
	Use:   "publish <item-id>",
	Short: "Publish an existing rd item's current state to the nostr log + relays",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		itemID := args[0]
		dir, ok := readyProjectDir()
		if !ok {
			return fmt.Errorf("no .ready project directory found")
		}
		// Load current item state from the JSONL projection.
		path := jsonlPath()
		if path == "" {
			return fmt.Errorf("no mutations.jsonl found")
		}
		campfireID, _, _ := projectRoot()
		items, err := state.DeriveFromJSONLWithCampfire(path, campfireID)
		if err != nil {
			return err
		}
		item, found := items[itemID]
		if !found {
			return fmt.Errorf("item %q not found in rd state", itemID)
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

// nostrReadyCmd is the ready-82c proof surface: it computes the SAME named-view
// readiness set as `rd ready`, but sourced entirely from the nostr projection
// (ProjectItems over the local authoritative log, optionally cache-filled from
// relays) instead of the campfire-backed derive path. Filtering, scoping-by-
// identity, sorting and output formatting are intentionally identical to
// readyCmd (cmd/rd/ready.go) — this is a substrate swap, not a new feature.
var nostrReadyCmd = &cobra.Command{
	Use:   "ready",
	Short: "Compute the attention-engine readiness set from the nostr projection (ready-82c)",
	Long: `Like 'rd ready', but the item set is projected from the nostr event log
instead of the legacy-backed store. Proves the dependency- and gate-aware
attention engine (pkg/views + pkg/state.Item) computes the same readiness set
regardless of substrate.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		viewName, _ := cmd.Flags().GetString("view")
		reconcileFlag, _ := cmd.Flags().GetBool("reconcile")

		dir, ok := readyProjectDir()
		if !ok {
			return fmt.Errorf("no .ready project directory found")
		}
		k, err := nostrKey()
		if err != nil {
			return err
		}
		log := rdSync.NewNostrLog(rdSync.NostrLogPath(dir))

		if reconcileFlag {
			ctx := context.Background()
			// ready-7ec: scope the relay reconcile to the PINNED board when one is
			// set, so `rd nostr ready` pulls only THIS project's board instead of the
			// relay's entire portfolio (every project, every board) — the same fix
			// applied to `rd nostr sync`. Unpinned installs get "" -> ReconcileAll's
			// unscoped behaviour, unchanged.
			r, err := rdSync.ReconcileBoard(ctx, nostrReadRelays(), log, nostrPinnedBoard(dir), nostrTrustSet(dir, k.PubKeyHex()), nostr.DefaultTimeout)
			if err != nil {
				return err
			}
			if debugOutput {
				fmt.Fprintf(os.Stderr, "nostr: reconcile-all fetched=%d added=%d relay_errors=%d\n", r.Fetched, r.Added, len(r.RelayErrors))
			}
		}

		events, err := log.ReadAll()
		if err != nil {
			return err
		}
		trusted := nostrTrustSet(dir, k.PubKeyHex())
		// Status-authority = author OR board maintainer (board-derived in ProjectItems,
		// ready-b57); read-trust = full set. Maintainers left nil.
		itemsByID := rdSync.ProjectItems(events, rdSync.ProjectOptions{
			Trusted:     trusted,
			PinnedBoard: nostrPinnedBoard(dir),
		})
		items := make([]*state.Item, 0, len(itemsByID))
		for _, item := range itemsByID {
			items = append(items, item)
		}

		if viewName == "" {
			viewName = views.ViewReady
		}
		filter := views.Named(viewName, k.PubKeyHex())
		if filter == nil {
			return fmt.Errorf("unknown view %q: choose from %v", viewName, views.AllNames())
		}
		items = views.Apply(items, filter)
		sortByPriorityETA(items)

		if jsonOutput {
			return outputItemsJSON(items)
		}
		for _, item := range items {
			fmt.Println(item.ID)
		}
		return nil
	},
}

// nostrSeedDemoCmd is ground-source proof infrastructure for ready-82c: it
// publishes a small, fixed dep+gate graph (matching
// pkg/sync.TestNostrProjection_ReadinessParity) directly to the local nostr
// log + configured relays. It exists ONLY to drive the demo shell script
// (scripts/demo_nostr_readiness.sh) against a LIVE relay -- it is not a
// production write path (rd dep add / rd gate do not publish nostr events
// today; that plumbing is out of scope for ready-82c, which is the read-side
// attention-engine substrate swap). Hidden from --help.
var nostrSeedDemoCmd = &cobra.Command{
	Use:    "seed-demo",
	Short:  "internal: seed a fixed dep+gate graph for the ready-82c live-relay demo",
	Hidden: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		pub, ok, err := nostrPublisher()
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("no project dir for publisher")
		}
		dir, _ := readyProjectDir()
		board := boardSpecForProject(dir, pub.Key.PubKeyHex())
		ctx := context.Background()
		cards := []rdSync.CardSpec{
			{ItemID: "ready-t01", Title: "t01", Status: state.StatusActive, Priority: "p0", Type: "task", BoardD: board.BoardD},
			{ItemID: "ready-t02", Title: "t02", Status: state.StatusActive, Priority: "p1", Type: "task", BoardD: board.BoardD, Deps: []string{"ready-t01"}},
			{ItemID: "ready-t03", Title: "t03", Status: state.StatusWaiting, Priority: "p1", Type: "task", BoardD: board.BoardD, Gate: "human", WaitingType: "gate", WaitingOn: "needs sign-off"},
			{ItemID: "ready-t04", Title: "t04", Status: state.StatusDone, Priority: "p2", Type: "task", BoardD: board.BoardD},
			{ItemID: "ready-t05", Title: "t05", Status: state.StatusActive, Priority: "p2", Type: "task", BoardD: board.BoardD, Deps: []string{"ready-t04"}},
		}
		now := time.Now().Unix()
		for i, card := range cards {
			var boardArg *rdSync.BoardSpec
			if i == 0 {
				boardArg = &board // publish the board once, alongside the first card
			}
			res, err := pub.PublishItem(ctx, boardArg, card, now+int64(i))
			if err != nil {
				return fmt.Errorf("publish %s: %w", card.ItemID, err)
			}
			for _, ev := range res.Events {
				fmt.Printf("published %s kind %d id %s relay-accepted=%v\n", card.ItemID, ev.Kind, ev.EventID, ev.AnyRelay)
			}
		}
		return nil
	},
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
		fmt.Printf("flush: total=%d flushed=%d remaining=%d relay_errors=%d\n",
			res.Total, res.Flushed, res.Remaining, len(res.RelayErrors))
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

// nostrReadEnabled reports whether DUAL-READ resolves rd items from the nostr
// projection instead of the campfire/JSONL backend. It is OFF by default so
// campfire stays the authoritative default backend (the operator's explicit
// non-destructive scope for ready-d65): the live campfire-backed rd the
// orchestrator runs is never affected. RD_NOSTR_READ=1 flips a SINGLE process to
// read from the nostr log — the controlled, nostr-only verification context.
func nostrReadEnabled() bool { return os.Getenv("RD_NOSTR_READ") == "1" }

// nostrProjectAllItems reads the local authoritative nostr log and returns the
// projected item set (both as a slice and by id). This is the read side of
// dual-read and the "projected" side of the parity proof: a pure replay of the
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
	// ready-b57: status-authority is board-derived (author OR board maintainer),
	// NOT the whole trust set. Read-trust stays the full set; Maintainers left nil.
	byID := rdSync.ProjectItems(events, rdSync.ProjectOptions{Trusted: trusted, PinnedBoard: nostrPinnedBoard(dir)})
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

// nostrMigrateCmd re-emits the CURRENT campfire rd item set as nostr events —
// the ready-d65 CUTOVER migration (non-destructive scope). It reads the SOURCE
// items from the DEFAULT campfire/JSONL backend (never from nostr — that would be
// circular), then for EACH item builds the board (once), a 30302 card
// materializing current state, and one NIP-34 status event per history entry
// (original timestamp, close-reason, and ORIGINAL actor via the "by" tag). Every
// event is appended to the local authoritative log and best-effort published to
// the locked write relays with the allowlisted portfolio key. Idempotent by event
// id: re-running adds nothing. Campfire is untouched and stays authoritative.
var nostrMigrateCmd = &cobra.Command{
	Use:   "migrate",
	Short: "Re-emit the current legacy item set as nostr events (ready-d65 cutover, non-destructive)",
	Long: `Read the current legacy (JSONL) rd item set and re-emit every item as nostr
events (30301 board, 30302 card, NIP-34 status log) into the local authoritative
log and the locked write relays, preserving id, status, priority, type, deps,
gates, full history + close-reasons, and provenance. The legacy source is NOT
modified. Idempotent by event id (safe to re-run).`,
	RunE: func(cmd *cobra.Command, args []string) error {
		localOnly, _ := cmd.Flags().GetBool("local-only")
		limit, _ := cmd.Flags().GetInt("limit")
		includeTerminal, _ := cmd.Flags().GetBool("all")
		only, _ := cmd.Flags().GetStringSlice("only")

		// SOURCE = campfire/JSONL (the default backend), NEVER the nostr projection.
		src, err := allItemsFromJSONLOrStore()
		if err != nil {
			return fmt.Errorf("loading campfire source items: %w", err)
		}
		// Deterministic order (by id) so a limited/sample run is reproducible.
		sort.Slice(src, func(i, j int) bool { return src[i].ID < src[j].ID })
		if len(only) > 0 {
			want := make(map[string]bool, len(only))
			for _, id := range only {
				want[id] = true
			}
			filtered := src[:0:0]
			for _, it := range src {
				if want[it.ID] {
					filtered = append(filtered, it)
				}
			}
			src = filtered
		}
		if !includeTerminal {
			// --all defaults true: a migration that dropped done/cancelled items would
			// lose their audit trail. --all=false is an explicit opt-out to migrate an
			// OPEN-only subset (e.g. a fast sample), so honour it by excluding terminal
			// items rather than silently ignoring the flag.
			open := src[:0:0]
			for _, it := range src {
				if !state.IsTerminal(it) {
					open = append(open, it)
				}
			}
			src = open
		}
		if limit > 0 && limit < len(src) {
			src = src[:limit]
		}

		dir, _ := readyProjectDir()
		k, err := nostrKey()
		if err != nil {
			return err
		}

		// ready-14b — PIN THE SIGNER TO THE BOARD OWNER before re-emitting anything.
		// migrate re-authors the 30301 board (the trust ROOT) signed by k, the ACTIVE
		// $RD_ACTOR key, and stamps every card's board-membership under k's pubkey. If
		// k is NOT the board owner, the whole projection is rooted on the wrong key: the
		// board author (implicit level-2 trust anchor, checker.go bootstrap) becomes an
		// agent, every migrated card is attributed to the wrong owner, and authz breaks
		// portfolio-wide — silently. Two ways k can be wrong, both refused here BEFORE
		// any event is built/published (fail-closed, nothing half-migrated):
		//  1. A named agent actor ($RD_ACTOR != "owner") signs with an agent key that is
		//     definitionally not the owner — refused even on a fresh (unpinned) bootstrap
		//     migration where nostrBoardAuthor would otherwise fall back to the signer.
		//  2. Even as the owner actor, if the board is already PINNED to a specific owner
		//     pubkey and this machine's owner key is not that pubkey (wrong machine / a
		//     regenerated key), nostrBoardAuthor resolves the pinned owner and the signer
		//     mismatch is caught.
		signer := k.PubKeyHex()
		if a := rdActor(); a != nostr.OwnerActor {
			return fmt.Errorf("rd migrate: refusing to migrate as actor %q — the migration re-authors the 30301 board (the trust root) and only the OWNER may sign it; "+
				"a non-owner key would mis-bind the trust root and attribute every migrated item to the wrong owner. Re-run as the owner (unset $RD_ACTOR, or set RD_ACTOR=owner on the owner's machine)", a)
		}
		boardAuthor, err := nostrBoardAuthor(dir, signer)
		if err != nil {
			return err
		}
		if signer != boardAuthor {
			return fmt.Errorf("rd migrate: refusing to migrate — the active owner signing key (pubkey %s) is NOT the owner of the pinned target board (owner pubkey %s); "+
				"migrating under it would re-author the 30301 board and mis-bind the trust root. Run migrate on the machine holding the pinned board owner's key", signer, boardAuthor)
		}

		log := rdSync.NewNostrLog(rdSync.NostrLogPath(dir))
		boardD := projectPrefix(dir)

		var relays []string
		if !localOnly {
			relays = nostrWriteRelays()
		}
		pub := &rdSync.Publisher{
			Key:         k,
			Log:         log,
			WriteRelays: relays,
			PendingPath: nostrPendingPath(dir),
		}

		// Build the WHOLE event stream first (board once + per-item card+history),
		// then append in a SINGLE AppendUnique pass. Per-item AppendUnique would be
		// O(N^2) (each call re-reads the growing log); one batched pass is O(N) and
		// keeps the re-run-safe dedup.
		// Board stamped at a DETERMINISTIC created_at (not time.Now) so a re-run
		// re-derives the identical board event id and AppendUnique dedups it — else
		// every migration re-run would append one fresh board line (breaking on-disk
		// idempotence). Use the earliest source item's second; fall back to 1.
		board := rdSync.BoardSpec{BoardD: boardD, Title: boardD, Maintainers: []string{k.PubKeyHex()}}
		boardAt := int64(1)
		for _, item := range src {
			if s := item.CreatedAt / int64(time.Second); s > 0 && (boardAt == 1 || s < boardAt) {
				boardAt = s
			}
		}
		be, err := rdSync.BuildBoardEvent(k, board, boardAt)
		if err != nil {
			return err
		}
		allEvents := []*nostr.Event{be}
		migrated := 0
		for _, item := range src {
			evs, err := rdSync.BuildItemMigrationEvents(k, boardD, item)
			if err != nil {
				return fmt.Errorf("building events for %s: %w", item.ID, err)
			}
			allEvents = append(allEvents, evs...)
			migrated++
		}
		ctx := context.Background()
		res, added, err := pub.PublishEventsUnique(ctx, allEvents)
		if err != nil {
			return fmt.Errorf("publishing migration events: %w", err)
		}
		events := len(allEvents)
		buffered := 0
		if res.Buffered {
			buffered = 1
		}

		if jsonOutput {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(map[string]any{
				"migrated_items": migrated, "events": events, "appended": added,
				"buffered": buffered == 1, "relays": relays, "local_only": localOnly,
			})
		}
		fmt.Printf("migrated %d items -> %d events (%d newly appended; board+cards+status log)\n", migrated, events, added)
		if localOnly {
			fmt.Println("(local-only: appended to the authoritative log; no relay publish)")
		} else if buffered == 1 {
			fmt.Println("(some events reached no relay; buffered to nostr-pending.jsonl — durable in local log)")
		} else {
			fmt.Printf("published to relays: %v\n", relays)
		}
		return nil
	},
}

// nostrParityCmd proves item-for-item parity between the campfire SOURCE and the
// nostr PROJECTION (ready-d65 DONE#3). It derives the source from campfire/JSONL,
// projects the nostr log, and compares every item on count, status, priority,
// type, deps, gate, history length + close-reasons, and provenance. Exits non-zero
// on ANY mismatch (a lost or silently altered item). This is the ground-source
// gate: NEVER fabricated — it reads the real 174/1565 live items.
var nostrParityCmd = &cobra.Command{
	Use:   "parity",
	Short: "Assert item-for-item parity: legacy source == nostr projection (ready-d65)",
	RunE: func(cmd *cobra.Command, args []string) error {
		showAll, _ := cmd.Flags().GetBool("verbose")
		sample, _ := cmd.Flags().GetBool("sample")

		srcSlice, err := allItemsFromJSONLOrStore()
		if err != nil {
			return fmt.Errorf("loading campfire source items: %w", err)
		}
		src := make(map[string]*state.Item, len(srcSlice))
		for _, it := range srcSlice {
			src[it.ID] = it
		}
		_, projected, err := nostrProjectAllItems()
		if err != nil {
			return err
		}
		// UNDERCOUNT IS A HARD MISMATCH (ready-187). Previously, whenever the
		// projection held FEWER items than the source the CLI silently narrowed the
		// comparison to the projected id set — assuming an intentional `migrate --limit`
		// sample. That masked GENUINELY LOST items: a migration that dropped 200 items
		// would still report "parity: all matched" over the 1365 that survived. The
		// narrowing is now OPT-IN via --sample. Without it, an incomplete projection is
		// a parity FAILURE: CompareItemSets compares the FULL source, so every missing
		// id is reported as a LOST item and the non-zero exit fires.
		compareSrc := parityCompareSource(src, projected, sample)
		rep := rdSync.CompareItemSets(compareSrc, projected)

		if jsonOutput {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			if err := enc.Encode(rep); err != nil {
				return err
			}
		} else {
			fmt.Printf("PARITY: source=%d projected=%d matched=%d mismatched=%d\n",
				rep.SourceCount, rep.ProjectedCount, rep.Matched, rep.Mismatched)
			for _, ip := range rep.Items {
				if !ip.Match() {
					fmt.Printf("  MISMATCH %s: %v\n", ip.ItemID, ip.Diffs)
				} else if showAll {
					fmt.Printf("  ok       %s\n", ip.ItemID)
				}
			}
		}
		if !rep.AllMatch() {
			return fmt.Errorf("parity FAILED: %d mismatched item(s)", rep.Mismatched)
		}
		return nil
	},
}

// parityCompareSource decides which source items the parity comparison runs over
// (ready-187). Default: the FULL source, so a projection missing any source id fails
// parity as a LOST item — an undercount can never be silently masked. Only when the
// operator explicitly asserts an intentional subset (sample=true) AND the projection
// is genuinely smaller does it narrow to the projected ids (the legitimate
// `migrate --limit` case). This is a pure function so the undercount-fails behaviour
// is unit-testable without a live store.
func parityCompareSource(src, projected map[string]*state.Item, sample bool) map[string]*state.Item {
	if !sample || len(projected) >= len(src) {
		return src
	}
	narrowed := make(map[string]*state.Item, len(projected))
	for id := range projected {
		if it, ok := src[id]; ok {
			narrowed[id] = it
		}
	}
	return narrowed
}

func init() {
	nostrShowCmd.Flags().Bool("reconcile", false, "cache-fill from read relays before reconstructing (local log stays authoritative)")
	nostrReadyCmd.Flags().String("view", "ready", "named view: ready, work, pending, overdue, gates")
	nostrReadyCmd.Flags().Bool("reconcile", false, "cache-fill ALL items from read relays before computing readiness")
	nostrPutCmd.Flags().String("title", "", "item title")
	nostrPutCmd.Flags().String("status", "", "rd status (default active)")
	nostrPutCmd.Flags().String("priority", "", "priority (p0..p3)")
	nostrPutCmd.Flags().String("type", "task", "item type")
	nostrPutCmd.Flags().String("context", "", "item description/context (card content)")
	nostrPutCmd.Flags().String("note", "", "status-change reason (rd close-with-reason); publishes a status change instead of a fresh card")
	nostrMigrateCmd.Flags().Bool("local-only", false, "append to the local authoritative log only; skip relay publish")
	nostrMigrateCmd.Flags().Int("limit", 0, "migrate at most N items (0 = all); deterministic by id — used to seed a live-relay sample")
	nostrMigrateCmd.Flags().StringSlice("only", nil, "migrate ONLY these item ids (comma-separated) — used to publish a dep-closed live-relay sample")
	nostrMigrateCmd.Flags().Bool("all", true, "include terminal items (done/cancelled/failed) — default true so history is not lost")
	nostrParityCmd.Flags().Bool("verbose", false, "print every item (matched and mismatched), not just mismatches")
	nostrParityCmd.Flags().Bool("sample", false, "the projection is an intentional subset (e.g. from 'migrate --limit'): compare only the projected ids instead of failing on the missing source items. WITHOUT this flag, projected<source is a HARD parity FAILURE (a lost item), never silently narrowed.")
	nostrCmd.AddCommand(nostrMigrateCmd)
	nostrCmd.AddCommand(nostrParityCmd)
	nostrCmd.AddCommand(nostrShowCmd)
	nostrCmd.AddCommand(nostrPublishCmd)
	nostrCmd.AddCommand(nostrReadyCmd)
	nostrCmd.AddCommand(nostrSeedDemoCmd)
	nostrCmd.AddCommand(nostrPutCmd)
	// nostrSyncCmd is NOT registered here: `rd nostr sync` was promoted to the
	// top-level `rd sync` (ready-9ac). The command var stays as the reused
	// substrate for that top-level surface (cmd/rd/sync.go).
	nostrCmd.AddCommand(nostrFlushCmd)
	nostrCmd.AddCommand(nostrMergeCmd)
	rootCmd.AddCommand(nostrCmd)
}
