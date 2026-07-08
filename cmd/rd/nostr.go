package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
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

// nostrKey loads (or first-run creates) the portfolio secp256k1 signing key.
func nostrKey() (*nostr.Key, error) {
	return nostr.LoadOrCreatePortfolioKey(nostr.DefaultKeyPath(CFHome()))
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

// boardSpecForProject returns the NIP-100 board (project) spec for the current
// project directory, with the portfolio key as sole maintainer.
func boardSpecForProject(dir, pubkey string) rdSync.BoardSpec {
	name := projectPrefix(dir)
	return rdSync.BoardSpec{BoardD: name, Title: name, Maintainers: []string{pubkey}}
}

// publishItemCreateNostr is the create-time hook: when nostr is enabled it
// publishes the board + card + status events for a freshly created item and
// appends them to the local authoritative log. It is best-effort — a relay
// failure never fails `rd create` (the event is durable in the log; the campfire
// / JSONL write already succeeded). Returns nil when nostr is disabled.
func publishItemCreateNostr(itemID, title, itemType, priority, status, itemContext, forParty string) error {
	if !nostrEnabled() {
		return nil
	}
	pub, ok, err := nostrPublisher()
	if err != nil || !ok {
		return err
	}
	dir, _ := readyProjectDir()
	board := boardSpecForProject(dir, pub.Key.PubKeyHex())
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
	}
	ctx := context.Background()
	res, err := pub.PublishItem(ctx, &board, card, time.Now().Unix())
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
			r, err := rdSync.ReconcileItem(ctx, nostrReadRelays(), log, itemID, k.PubKeyHex(), nostr.DefaultTimeout)
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
		items := rdSync.ProjectItems(events, rdSync.ProjectOptions{
			Maintainers: map[string]bool{k.PubKeyHex(): true},
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
		board := boardSpecForProject(dir, pub.Key.PubKeyHex())
		card := rdSync.CardSpec{
			ItemID:      item.ID,
			Title:       item.Title,
			Status:      item.Status,
			Priority:    item.Priority,
			Type:        item.Type,
			Context:     item.Context,
			BoardD:      board.BoardD,
			Deps:        item.BlockedBy,
			Gate:        item.Gate,
			WaitingType: item.WaitingType,
			WaitingOn:   item.WaitingOn,
		}
		res, err := pub.PublishItem(context.Background(), &board, card, time.Now().Unix())
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
instead of the campfire-backed store. Proves the dependency- and gate-aware
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
			r, err := rdSync.ReconcileAll(ctx, nostrReadRelays(), log, k.PubKeyHex(), nostr.DefaultTimeout)
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
		itemsByID := rdSync.ProjectItems(events, rdSync.ProjectOptions{
			Maintainers: map[string]bool{k.PubKeyHex(): true},
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

func init() {
	nostrShowCmd.Flags().Bool("reconcile", false, "cache-fill from read relays before reconstructing (local log stays authoritative)")
	nostrReadyCmd.Flags().String("view", "ready", "named view: ready, work, pending, overdue, gates")
	nostrReadyCmd.Flags().Bool("reconcile", false, "cache-fill ALL items from read relays before computing readiness")
	nostrCmd.AddCommand(nostrShowCmd)
	nostrCmd.AddCommand(nostrPublishCmd)
	nostrCmd.AddCommand(nostrReadyCmd)
	nostrCmd.AddCommand(nostrSeedDemoCmd)
	rootCmd.AddCommand(nostrCmd)
}
