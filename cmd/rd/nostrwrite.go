package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/campfire-net/ready/pkg/state"
	rdSync "github.com/campfire-net/ready/pkg/sync"
)

// ============================================================================
// ready-6ef S-write — nostr-native DEFAULT write path.
//
// A project initialized by the default `rd init` (initNostr) pins a board
// coordinate in .ready/config.json and keeps its work items as signed secp256k1
// events in .ready/nostr-log.jsonl. On THIS path every mutation:
//
//   - signs with the secp256k1 OWNER key ($RD_HOME, pkg/nostr) — never a .cf
//     ed25519 identity;
//   - resolves the target item from the nostr PROJECTION (the local authoritative
//     log), never from the campfire store / mutations.jsonl;
//   - treats the local-log append performed by the publish* helpers as the PRIMARY
//     durable write (relay reachability is irrelevant — a relay failure buffers,
//     never fails the mutation, but a LOG append failure IS fatal here);
//   - never calls requireAgentAndStore / requireExecutor / protocol.Init, so no
//     .cf/identity.json is ever created or read.
//
// item.By is set to the secp256k1 SIGNING pubkey; audit ChangedBy is derived by
// the projection from each status event's PubKey (also the secp256k1 signer) —
// see pkg/sync/nostrproject.go. The campfire executor code stays PRESENT but
// UNINVOKED on this path (I7/ready-cb6 deletes it); campfire-backed and --offline
// JSONL-only projects keep their existing code paths untouched.
// ============================================================================

// errNotNostrProject is returned by every write command when the current directory
// is not a nostr-native ready project. After the campfire cutover (ready-cb6) the
// nostr-native local signed-event log is the ONLY write path — there is no campfire
// executor and no --offline JSONL writer to fall back to.
func errNotNostrProject() error {
	return fmt.Errorf("not a ready project: run 'rd init --name <project>' to create one")
}

// nostrNativeProject reports whether the current project uses the nostr-native
// default write path. It returns (projectDir, true) exactly when a .ready/ project
// exists AND a board coordinate is pinned in its config.json — the on-disk
// signature the default `rd init` (initNostr) leaves. A directory with no pinned
// board returns ("", false) and every write command errors via errNotNostrProject.
func nostrNativeProject() (string, bool) {
	dir, ok := readyProjectDir()
	if !ok {
		return "", false
	}
	if nostrPinnedBoard(dir) == "" {
		return "", false
	}
	return dir, true
}

// nostrWriteActive reports whether the rd->nostr publish helpers must run as the
// PRIMARY durable write. True whenever RD_NOSTR is explicitly set (the legacy
// best-effort mirror on a campfire project) OR the project is nostr-native (the
// cutover default path). Only a campfire-backed / --offline project with RD_NOSTR
// unset skips publishing — preserving pre-cutover behaviour for those paths.
func nostrWriteActive() bool {
	if nostrEnabled() {
		return true
	}
	_, native := nostrNativeProject()
	return native
}

// nostrReadActive reports whether the DUAL-READ surface (list/ready/show) must
// resolve items from the nostr projection instead of the campfire/JSONL backend.
// It is the read-side mirror of nostrWriteActive (ready-6ef S-read): true whenever
// RD_NOSTR_READ is explicitly set (the legacy single-process verification context)
// OR the project is nostr-native (the cutover DEFAULT path — a pinned board in
// .ready/config.json). Campfire-backed and --offline JSONL-only projects with
// RD_NOSTR_READ unset keep reading their existing backend, so the campfire
// executor/read path stays selectable (present-but-uninvoked on the default path)
// until I7 deletes it. This makes `rd list/ready/show` read the nostr projection
// by DEFAULT on an `rd init` project with NO env set — DONE#3 of the cutover.
func nostrReadActive() bool {
	if nostrReadEnabled() {
		return true
	}
	_, native := nostrNativeProject()
	return native
}

// nostrSelfPubkey returns the secp256k1 signing pubkey (hex) for the current
// actor — the sole attribution root on the nostr-native path.
func nostrSelfPubkey() (string, error) {
	k, err := nostrKey()
	if err != nil {
		return "", err
	}
	return k.PubKeyHex(), nil
}

// nostrResolveItem projects the local authoritative nostr log and resolves an
// item by exact id, falling back to a UNIQUE prefix match (mirroring the
// campfire/JSONL resolvers' prefix expansion). Fail-closed: an unknown id, or an
// ambiguous prefix, is an error.
func nostrResolveItem(itemID string) (*state.Item, error) {
	_, byID, err := nostrProjectAllItems()
	if err != nil {
		return nil, err
	}
	if it, ok := byID[itemID]; ok {
		return it, nil
	}
	var match *state.Item
	for id, it := range byID {
		if strings.HasPrefix(id, itemID) {
			if match != nil {
				return nil, fmt.Errorf("item prefix %q is ambiguous in the nostr projection", itemID)
			}
			match = it
		}
	}
	if match == nil {
		return nil, fmt.Errorf("item %q not found in the nostr projection", itemID)
	}
	return match, nil
}

// nostrExistingIDs returns the set of item ids currently in the nostr projection,
// for create-time collision detection.
func nostrExistingIDs() (map[string]struct{}, error) {
	_, byID, err := nostrProjectAllItems()
	if err != nil {
		return nil, err
	}
	out := make(map[string]struct{}, len(byID))
	for id := range byID {
		out[id] = struct{}{}
	}
	return out, nil
}

// publishItemFullCreateNostr publishes a freshly-created item as board (once) +
// 30302 card + NIP-34 status(inbox) events for the WHOLE item (all card fields via
// CardSpecFromItem), signing with the secp256k1 key and appending to the local
// authoritative log as the primary durable write. Unlike publishItemCreateNostr
// (which carries only a handful of fields) this materializes level / parent / eta /
// due / labels / assignee at create time so the latest-wins card is complete
// without a follow-up republish. Returns an error (fatal on the native path) when
// the log append fails.
func publishItemFullCreateNostr(dir, signer string, item *state.Item) error {
	pub, ok, err := nostrPublisher()
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("no .ready project directory for nostr publisher")
	}
	boardAuthor := nostrBoardAuthor(dir, signer)
	board := boardSpecForProject(dir, boardAuthor)
	card := rdSync.CardSpecFromItem(item, board.BoardD)
	card.BoardAuthor = boardAuthor
	// Only the board AUTHOR (owner) may sign the owner's 30301 board; an agent's
	// card still joins the owner's board via BoardAuthor above.
	var boardArg *rdSync.BoardSpec
	if signer == boardAuthor {
		boardArg = &board
	}
	res, err := pub.PublishItem(context.Background(), boardArg, card, nostrNextCreatedAt(pub.Log, rdSync.ItemDriftScope(item.ID)))
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

// emitMutationResult writes the standard mutation result: a JSON object (when
// --json) carrying at least the item id plus any command-specific extras, else a
// human line. On the nostr-native path there is no campfire message id, so the
// JSON deliberately omits campfire_id/msg_id.
func emitMutationResult(humanLine string, extras map[string]any) error {
	if jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(extras)
	}
	fmt.Println(humanLine)
	return nil
}

// ----------------------------------------------------------------------------
// Per-command nostr-native mutation bodies.
// ----------------------------------------------------------------------------

// runClaimNostr transitions an item to active with by=signer.
func runClaimNostr(itemID, reason string) error {
	self, err := nostrSelfPubkey()
	if err != nil {
		return err
	}
	item, err := nostrResolveItem(itemID)
	if err != nil {
		return err
	}
	if state.IsTerminal(item) {
		return fmt.Errorf("item %s is already %s", item.ID, item.Status)
	}
	item.Status = state.StatusActive
	item.By = self
	if err := publishItemStatusChangeNostr(item, reason); err != nil {
		return fmt.Errorf("nostr publish (claim): %w", err)
	}
	return emitMutationResult(fmt.Sprintf("claimed %s", item.ID), map[string]any{"id": item.ID})
}

// runCloseNostr transitions an item to a terminal status carrying the reason, and
// re-publishes every card this item was blocking (implicit unblock parity).
func runCloseNostr(itemID, resolution, reason, humanVerb string) error {
	item, err := nostrResolveItem(itemID)
	if err != nil {
		return err
	}
	if state.IsTerminal(item) {
		return fmt.Errorf("item %s is already %s", item.ID, item.Status)
	}
	blockedByThis := item.Blocks
	item.Status = closeResolutionToStatus(resolution)
	if err := publishItemStatusChangeNostr(item, reason); err != nil {
		return fmt.Errorf("nostr publish (close): %w", err)
	}
	publishImplicitUnblockNostrNative(blockedByThis)
	return emitMutationResult(fmt.Sprintf("%s %s (%s)", humanVerb, item.ID, resolution),
		map[string]any{"id": item.ID, "resolution": resolution})
}

// runDelegateNostr reassigns an item's performer (by=to), publishing a status
// change so the reassignment lands in the audit-history replay. Closes the
// delegate publisher GAP (delegate previously published NO nostr event).
func runDelegateNostr(itemID, to, reason string) error {
	item, err := nostrResolveItem(itemID)
	if err != nil {
		return err
	}
	if state.IsTerminal(item) {
		return fmt.Errorf("item %s is already %s", item.ID, item.Status)
	}
	item.By = to
	if err := publishItemStatusChangeNostr(item, reason); err != nil {
		return fmt.Errorf("nostr publish (delegate): %w", err)
	}
	return emitMutationResult(fmt.Sprintf("delegated %s to %s", item.ID, to),
		map[string]any{"id": item.ID, "to": to})
}

// runGateNostr transitions an item to waiting (waiting_type=gate) carrying the
// gate description as the reason.
func runGateNostr(itemID, gateType, description string) error {
	item, err := nostrResolveItem(itemID)
	if err != nil {
		return err
	}
	if state.IsTerminal(item) {
		return fmt.Errorf("item %s is already %s", item.ID, item.Status)
	}
	item.Status = state.StatusWaiting
	item.Gate = gateType
	item.WaitingType = "gate"
	item.WaitingOn = description
	if err := publishItemStatusChangeNostr(item, description); err != nil {
		return fmt.Errorf("nostr publish (gate): %w", err)
	}
	// Re-resolve so msg_id reports the projection-derived gate event id — the same
	// value `rd show`/`rd gates` surface for this pending gate (GateMsgID).
	var gateMsgID string
	if gated, rerr := nostrResolveItem(itemID); rerr == nil {
		gateMsgID = gated.GateMsgID
	}
	return emitMutationResult(fmt.Sprintf("gate sent for %s (%s)", item.ID, gateType),
		map[string]any{"id": item.ID, "gate_type": gateType, "msg_id": gateMsgID})
}

// runApproveNostr resolves a pending gate: back to active, gate/waiting cleared.
func runApproveNostr(itemID, reason string) error {
	item, err := nostrResolveItem(itemID)
	if err != nil {
		return err
	}
	if item.GateMsgID == "" && item.Gate == "" && item.WaitingType != "gate" {
		return fmt.Errorf("item %s has no pending gate to approve", item.ID)
	}
	if item.Status != state.StatusWaiting {
		return fmt.Errorf("item %s is not waiting (status=%s)", item.ID, item.Status)
	}
	item.Status = state.StatusActive
	item.Gate = ""
	item.WaitingType = ""
	item.WaitingOn = ""
	item.WaitingSince = ""
	item.GateMsgID = ""
	if err := publishItemStatusChangeNostr(item, reason); err != nil {
		return fmt.Errorf("nostr publish (approve): %w", err)
	}
	return emitMutationResult(fmt.Sprintf("approved gate for %s", item.ID),
		map[string]any{"id": item.ID, "resolution": "approved"})
}

// runRejectNostr rejects a pending gate: the item REMAINS waiting, but the
// rejection reason is recorded in the audit-history replay via a status event that
// re-affirms waiting. Closes the reject publisher GAP (reject previously published
// NO nostr event).
func runRejectNostr(itemID, reason string) error {
	item, err := nostrResolveItem(itemID)
	if err != nil {
		return err
	}
	if item.GateMsgID == "" && item.Gate == "" && item.WaitingType != "gate" {
		return fmt.Errorf("item %s has no pending gate to reject", item.ID)
	}
	if item.Status != state.StatusWaiting {
		return fmt.Errorf("item %s is not waiting (status=%s)", item.ID, item.Status)
	}
	// Item stays waiting; publish the rejection as a status(waiting) event so the
	// ruling is preserved in history without transitioning out of the gate.
	if err := publishItemStatusChangeNostr(item, reason); err != nil {
		return fmt.Errorf("nostr publish (reject): %w", err)
	}
	return emitMutationResult(fmt.Sprintf("rejected gate for %s", item.ID),
		map[string]any{"id": item.ID, "resolution": "rejected"})
}

// runDepAddNostr wires blocker→blocked by appending the blocker id to the blocked
// item's dep set and re-publishing its card (card-only edit; blocked status is a
// projection of the "i" dep tags). Closes the dep publisher GAP on the native path.
func runDepAddNostr(blockedArg, blockerArg string) error {
	if state.IsCrossCampfireRef(blockedArg) {
		return fmt.Errorf("cross-project deps not supported for blocked item: %q looks like a cross-campfire reference", blockedArg)
	}
	blocked, err := nostrResolveItem(blockedArg)
	if err != nil {
		return fmt.Errorf("resolving blocked item %q: %w", blockedArg, err)
	}
	blocker, err := nostrResolveItem(blockerArg)
	if err != nil {
		return fmt.Errorf("resolving blocker item %q: %w", blockerArg, err)
	}
	blocked.BlockedBy = strSliceAppendUnique(blocked.BlockedBy, blocker.ID)
	if err := publishItemCardEditNostr(blocked); err != nil {
		return fmt.Errorf("nostr publish (dep add): %w", err)
	}
	return emitMutationResult(fmt.Sprintf("blocked: %s is now blocked by %s", blocked.ID, blocker.ID),
		map[string]any{"blocked_id": blocked.ID, "blocker_id": blocker.ID})
}

// runDepRemoveNostr drops blocker→blocked. On the nostr path deps are card "i"
// tags, so removal is a card-only edit with the blocker id stripped — no need to
// locate a work:block message.
func runDepRemoveNostr(blockedArg, blockerArg string) error {
	blocked, err := nostrResolveItem(blockedArg)
	if err != nil {
		return fmt.Errorf("resolving blocked item %q: %w", blockedArg, err)
	}
	blocker, err := nostrResolveItem(blockerArg)
	if err != nil {
		return fmt.Errorf("resolving blocker item %q: %w", blockerArg, err)
	}
	blocked.BlockedBy = strSliceRemove(blocked.BlockedBy, blocker.ID)
	if err := publishItemCardEditNostr(blocked); err != nil {
		return fmt.Errorf("nostr publish (dep remove): %w", err)
	}
	return emitMutationResult(fmt.Sprintf("unblocked: %s is no longer blocked by %s", blocked.ID, blocker.ID),
		map[string]any{"blocked_id": blocked.ID, "blocker_id": blocker.ID})
}

// runLabelAddNostr adds a label atom to an item (card-only edit).
func runLabelAddNostr(itemID, label string) error {
	item, err := nostrResolveItem(itemID)
	if err != nil {
		return err
	}
	item.Labels = strSliceAppendUnique(item.Labels, label)
	if err := publishItemCardEditNostr(item); err != nil {
		return fmt.Errorf("nostr publish (label add): %w", err)
	}
	return emitMutationResult(fmt.Sprintf("label %q added to %s", label, item.ID),
		map[string]any{"item_id": item.ID, "label": label})
}

// runLabelRemoveNostr removes a label atom from an item (card-only edit;
// removing an absent label is idempotent).
func runLabelRemoveNostr(itemID, label string) error {
	item, err := nostrResolveItem(itemID)
	if err != nil {
		return err
	}
	item.Labels = strSliceRemove(item.Labels, label)
	if err := publishItemCardEditNostr(item); err != nil {
		return fmt.Errorf("nostr publish (label remove): %w", err)
	}
	return emitMutationResult(fmt.Sprintf("label %q removed from %s", label, item.ID),
		map[string]any{"item_id": item.ID, "label": label})
}

// nostrUpdateSpec carries the resolved, normalized update fields for the
// nostr-native update path.
type nostrUpdateSpec struct {
	title, context, priority, eta, due, level string
	statusTo, waitingOn, waitingType, note    string
	hasFieldUpdate, hasStatusUpdate, claim    bool
}

// runUpdateNostr applies field edits and/or a status transition and/or a claim to
// an item on the nostr-native path, publishing a card-edit for pure field changes
// and a status event for each transition (mirroring update.go's campfire body).
func runUpdateNostr(itemID string, u nostrUpdateSpec) error {
	self, err := nostrSelfPubkey()
	if err != nil {
		return err
	}
	item, err := nostrResolveItem(itemID)
	if err != nil {
		return err
	}
	if state.IsTerminal(item) && u.hasFieldUpdate {
		return fmt.Errorf("item %s is already %s", item.ID, item.Status)
	}

	if u.hasFieldUpdate {
		if u.title != "" {
			item.Title = u.title
		}
		if u.context != "" {
			item.Context = u.context
		}
		if u.priority != "" {
			item.Priority = u.priority
		}
		if u.eta != "" {
			item.ETA = u.eta
		}
		if u.due != "" {
			item.Due = u.due
		}
		if u.level != "" {
			item.Level = u.level
		}
		if err := publishItemCardEditNostr(item); err != nil {
			return fmt.Errorf("nostr publish (update fields): %w", err)
		}
	}

	if u.hasStatusUpdate {
		item.Status = u.statusTo
		if u.waitingOn != "" {
			item.WaitingOn = u.waitingOn
		}
		if u.waitingType != "" {
			item.WaitingType = u.waitingType
		}
		if err := publishItemStatusChangeNostr(item, u.note); err != nil {
			return fmt.Errorf("nostr publish (update status): %w", err)
		}
	}

	if u.claim {
		item.Status = state.StatusActive
		item.By = self
		if err := publishItemStatusChangeNostr(item, ""); err != nil {
			return fmt.Errorf("nostr publish (update claim): %w", err)
		}
	}

	return emitMutationResult(fmt.Sprintf("updated %s", item.ID), map[string]any{"id": item.ID})
}

// nostrCreateSpec carries the resolved, normalized create fields.
type nostrCreateSpec struct {
	id, title, context, itemType, level, project string
	forParty, by, priority, parentID, eta, due   string
	labels                                       []string
	forChanged                                   bool
}

// runCreateNostr creates a new item on the nostr-native path: it derives the id
// (collision-checked against the projection), defaults --for to the signer, builds
// the full item, and publishes board+card+status(inbox) as the primary durable
// write. Returns the created id (printed bare when stdout is not a TTY, matching
// create.go's pipe-friendly contract).
func runCreateNostr(dir string, c nostrCreateSpec) (string, error) {
	self, err := nostrSelfPubkey()
	if err != nil {
		return "", err
	}
	forParty := c.forParty
	if !c.forChanged {
		forParty = self
	} else if forParty == "" {
		return "", fmt.Errorf("--for: value cannot be empty")
	}

	existing, err := nostrExistingIDs()
	if err != nil {
		return "", err
	}
	id := c.id
	if id == "" {
		generated, gerr := generateID(projectPrefix(dir), existing)
		if gerr != nil {
			return "", gerr
		}
		id = generated
	} else if _, collision := existing[id]; collision {
		return "", fmt.Errorf("item %q already exists", id)
	}

	item := &state.Item{
		ID:       id,
		Title:    c.title,
		Context:  c.context,
		Type:     c.itemType,
		Level:    c.level,
		Project:  c.project,
		For:      forParty,
		By:       c.by,
		Priority: c.priority,
		Status:   state.StatusInbox,
		ETA:      c.eta,
		Due:      c.due,
		ParentID: c.parentID,
		Labels:   c.labels,
	}
	if err := publishItemFullCreateNostr(dir, self, item); err != nil {
		return "", fmt.Errorf("nostr publish (create): %w", err)
	}
	return id, nil
}

// publishImplicitUnblockNostrNative re-publishes the card of every item the
// just-closed item was blocking, so the projection drops the now-stale dep edge —
// deps parity across the native close path. Unlike publishImplicitUnblockNostr
// (which resolves via byIDFromJSONLOrStore + a store), this resolves purely from
// the nostr projection.
func publishImplicitUnblockNostrNative(blockedIDs []string) {
	if len(blockedIDs) == 0 {
		return
	}
	for _, id := range blockedIDs {
		it, err := nostrResolveItem(id)
		if err != nil {
			continue
		}
		if err := publishItemCardEditNostr(it); err != nil {
			warnNostrPublishFailure(fmt.Sprintf("implicit-unblock %s", id), err)
		}
	}
}
