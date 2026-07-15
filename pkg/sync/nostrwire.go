// Nostr wire mapping (ready-a13) — the KEYSTONE mapping from an rd work item to
// nostr events. The rest of the rd->nostr migration builds on this file.
//
// WIRE MAPPING (established here, per epic ready-a14):
//
//	project / campfire   -> NIP-100 board  kind:30301 (addressable)
//	work item            -> NIP-100 card   kind:30302 (addressable, latest-wins)
//	status               -> "s" tag on the card + a NIP-34 status event
//	priority             -> "rank" tag (rd extension; also mirrored in "priority")
//	assignee             -> "p" tag
//
// AUTHORITY MODEL (epic design invariant, 2026-07-06):
//   - The local append-only SIGNED-EVENT LOG (pkg/sync NostrLog) is the source of
//     truth. Every event here is schnorr-signed via pkg/nostr.
//   - The 30302 addressable card is a materialized PROJECTION of the log
//     (fast reads + interop with nostr kanban clients). It is NEVER read back as
//     the source of truth — state is reconstructed by replaying the log.
//   - Status authority follows the NIP-34 rule: the most recent status event from
//     the item AUTHOR or a board MAINTAINER wins.
//
// HYBRID (not pure-addressable): we write BOTH the addressable card (current
// state materialization) AND the append-only status event (history), so the
// audit trail / close-with-reason / `rd show` replay survives. This is exactly
// campfire's snapshot+log pattern ported to nostr.
package sync

import (
	"fmt"
	"strings"

	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/state"
)

// Nostr event kinds used by the rd wire mapping. These follow the existing NIPs
// (do NOT invent kinds, per epic ready-a14):
//   - NIP-100 addressable board/card kinds.
//   - NIP-34 issue status kinds (1630 open .. 1633 draft).
const (
	// KindBoard is the NIP-100 addressable board event = an rd project/campfire.
	KindBoard = 30301
	// KindCard is the NIP-100 addressable card event = an rd work item.
	KindCard = 30302

	// KindStatusOpen is the NIP-34 "open" status kind. rd maps its non-terminal
	// statuses (inbox, active, scheduled, waiting, blocked) onto this kind and
	// preserves the EXACT rd status in the "status" tag.
	KindStatusOpen = 1630
	// KindStatusResolved is NIP-34 "resolved" — rd maps "done" here.
	KindStatusResolved = 1631
	// KindStatusClosed is NIP-34 "closed" — rd maps "cancelled" and "failed" here.
	KindStatusClosed = 1632
	// KindStatusDraft is NIP-34 "draft" (unused by rd today; reserved).
	KindStatusDraft = 1633

	// KindIssue is the NIP-34 "issue" root event (ready-da7). rd's status events
	// (1630-1632) anchor to the NIP-100 30302 card via "a"/"e" tags — that anchor
	// is rd's OWN projection's source of truth and is UNCHANGED. It is not,
	// however, a real NIP-34 issue: a generic NIP-34 issue-tracker client has no
	// reason to know what a 30302 card is, so it cannot associate rd's status
	// events with anything. Publishing one real kind:1621 issue event per item and
	// having the status event ALSO carry a NIP-10 "root"-marked "e" tag to it
	// (BuildStatusEventWithIssueRoot) is the standard, unambiguous NIP-34 pattern
	// generic clients already implement for patches/issues — this is purely
	// ADDITIVE alongside the existing card anchor, never a replacement for it.
	KindIssue = 1621
)

// statusKindFor maps an rd status string to the NIP-34 status kind. The exact rd
// status is preserved separately in a "status" tag, so richer rd statuses
// (waiting/blocked/scheduled) survive even though they all share KindStatusOpen.
func statusKindFor(rdStatus string) int {
	switch rdStatus {
	case state.StatusDone:
		return KindStatusResolved
	case state.StatusCancelled, state.StatusFailed:
		return KindStatusClosed
	default:
		// inbox, active, scheduled, waiting, blocked — all "open" in NIP-34 terms.
		return KindStatusOpen
	}
}

// CardSpec is the rd-item view fed into the wire mapping. It is a lean projection
// of the fields the 30302 card + status event carry. Callers (cmd/rd create, or
// the projection replay) build this from item state.
type CardSpec struct {
	// ItemID is the rd item ID (e.g. "ready-a13"); becomes the card "d" tag.
	ItemID string
	// Title becomes the card "title" tag.
	Title string
	// Status is the EXACT rd status (inbox, active, done, ...). Becomes the "s"
	// tag on the card and the "status" tag on the NIP-34 status event, and picks
	// the status event kind.
	Status string
	// Priority (p0..p3) becomes the "rank" tag (ordering) and a "priority" tag.
	Priority string
	// Assignee, when set, becomes a "p" tag (pubkey hex of the assignee).
	Assignee string
	// Type is the rd item type (task/decision/...); carried as an "itype" tag so
	// the projection can reconstruct it (rd extension over NIP-100).
	Type string
	// Context is the item's long description; carried in the card content.
	Context string
	// BoardD is the board (project) "d" identifier this card belongs to.
	BoardD string
	// BoardAuthor, when set, is the OWNER pubkey hex that authored the 30301 board
	// this card belongs to. The card's "a" board-membership coordinate is built
	// from it (30301:<BoardAuthor>:<BoardD>) instead of from the signing key — so a
	// card SIGNED by an AGENT key (whose pubkey differs from the owner's) still
	// references the OWNER's board coordinate and is therefore ACCEPTED by BP-3's
	// board pin (which pins the owner's coordinate). This DECOUPLES card AUTHORSHIP
	// (the signing key, still carried in the event's PubKey) from board MEMBERSHIP
	// (the owner's board coordinate). Empty preserves the pre-BP-4 behaviour exactly:
	// the "a" coordinate is the SIGNER's own board (BoardCoord(signer, BoardD)) — the
	// owner signing their own board, zero migration for existing single-key installs.
	BoardAuthor string

	// Deps lists the item IDs that BLOCK this item (rd dep add <this> <blocker>).
	// Each becomes a NIP-100 "i" (inter-card relationship) tag. NIP-100 leaves the
	// "i" tag's interpretation to the app; rd's reading is "the referenced item
	// must reach a terminal status before this one is unblocked" -- the same
	// semantics as the campfire work:block edge it replaces.
	Deps []string
	// Gate carries rd's escalation category (budget, design, scope, review,
	// human, stall) when set. Not part of NIP-34/NIP-100 -- an rd extension tag
	// ("gate"), per the epic's DEVIATIONS TO DECIDE list.
	Gate string
	// WaitingType / WaitingOn describe why status=waiting (e.g. "gate" pending
	// human resolution via rd gate/rd approve, or a routine wait like "vendor").
	// rd extension tags ("waiting_type" / "waiting_on"); not part of NIP-34/NIP-100.
	WaitingType string
	WaitingOn   string

	// Labels are the registry-validated label atoms attached to the item (rd
	// label add/remove). Each becomes a NIP-32 "l" label tag on the card so the
	// projection reconstructs Item.Labels. Additive rd-extension carrier (ready-2cf):
	// the write gate already validated each atom (pattern + registry membership),
	// so the card carries them verbatim — an unknown "l" tag is simply ignored by
	// non-rd readers.
	Labels []string
	// ETA carries the item's scheduled ETA (RFC3339) so `rd defer` round-trips on
	// nostr. rd-extension tag ("eta"); not part of NIP-34/NIP-100 (ready-2cf).
	ETA string

	// Level is the item's humanness/provenance level (Item.Level). Additive
	// rd-extension tag ("level"); not part of NIP-34/NIP-100 (ready-187). Old cards
	// without it project to an empty Level (sane default).
	Level string
	// For is the item's assignment SCOPE (Item.For) — who the work is FOR — which
	// the my-work / delegated views read. DISTINCT from Assignee/By (the `p` tag,
	// the actor). Additive rd-extension tag ("for") (ready-187). Without it the whole
	// assignment scope was silently dropped on nostr, breaking view parity.
	For string
	// ParentID is the parent item id (Item.ParentID) — the epic/child TREE edge.
	// Additive rd-extension tag ("parent") (ready-187). Without it the parent/child
	// tree was LOST on nostr. Kept as a plain rd-extension tag (not a NIP-100 a/e
	// coordinate) per the epic's additive-tag mandate — backward-compatible, and the
	// projection resolves it by item id exactly as campfire does.
	ParentID string
	// Due carries the item's hard due date (Item.Due, RFC3339). Additive rd-extension
	// tag ("due") (ready-187). DISTINCT from ETA. Old cards default to empty Due.
	Due string

	// Enc, when non-nil, puts this card in CONFIDENTIAL mode (epic ready-216): the
	// free-text fields (Title, Context, WaitingOn) are AEAD-sealed into
	// event.Content and the clear title/waiting_on tags are dropped; when Enc.LTK
	// is set the l label tags are HMAC-tokenized. Every relay-indexed routing tag
	// stays plaintext. Nil = plaintext mode, the exact pre-existing code path with
	// zero structural drift. Injected here so the write path is testable before
	// keydist (ready-a8a) supplies the CEK by unwrapping the owner-signed grant.
	Enc *Envelope
}

// BoardSpec describes an rd project/campfire as a NIP-100 board (30301).
type BoardSpec struct {
	// BoardD is the board addressable identifier (rd project prefix / campfire).
	BoardD string
	// Title is the human project name.
	Title string
	// Maintainers are pubkey hex strings that may author authoritative status
	// events for cards on this board (NIP-100 board "p" maintainers).
	Maintainers []string
}

// coord returns a NIP-01 addressable coordinate "<kind>:<pubkey>:<d>" used by
// "a" tags to reference an addressable event independent of any concrete event id.
func coord(kind int, pubkey, d string) string {
	return fmt.Sprintf("%d:%s:%s", kind, pubkey, d)
}

// BoardCoord returns the addressable coordinate of the board authored by pubkey.
func BoardCoord(pubkey, boardD string) string { return coord(KindBoard, pubkey, boardD) }

// CardCoord returns the addressable coordinate of the card authored by pubkey.
func CardCoord(pubkey, itemID string) string { return coord(KindCard, pubkey, itemID) }

// BuildBoardEvent constructs and signs the NIP-100 board (30301) event for a
// project/campfire. createdAt MUST be seconds (NIP-01) — the caller supplies it
// so id derivation is deterministic and testable.
func BuildBoardEvent(k *nostr.Key, spec BoardSpec, createdAt int64) (*nostr.Event, error) {
	tags := [][]string{
		{"d", spec.BoardD},
		{"title", spec.Title},
	}
	for _, m := range spec.Maintainers {
		if m != "" {
			tags = append(tags, []string{"p", m})
		}
	}
	e := &nostr.Event{
		Kind:      KindBoard,
		CreatedAt: createdAt,
		Tags:      tags,
		Content:   "",
	}
	if err := e.Sign(k); err != nil {
		return nil, fmt.Errorf("sync: sign board event: %w", err)
	}
	return e, nil
}

// cardBoardCoord returns the board-membership coordinate a CardSpec's card (and,
// per ready-7ec, its accompanying NIP-34 status events) reference: BoardAuthor's
// board when set, falling back to the signer's own board. This is exactly
// BuildCardEvent's original "a"-tag derivation, factored out so
// BuildStatusEventWithIssueRoot's callers can put the SAME coordinate on the
// status event that the card already carries — the two must agree, or a
// board-scoped filter would match one but not the other. Returns "" when
// spec.BoardD is empty (unscoped card), matching BuildCardEvent's own
// skip-the-tag behaviour.
func cardBoardCoord(k *nostr.Key, spec CardSpec) string {
	if spec.BoardD == "" {
		return ""
	}
	boardAuthor := spec.BoardAuthor
	if boardAuthor == "" {
		boardAuthor = k.PubKeyHex()
	}
	return BoardCoord(boardAuthor, spec.BoardD)
}

// BuildCardEvent constructs and signs the NIP-100 card (30302) event that
// materializes the item's CURRENT state (latest-wins addressable projection).
// createdAt MUST be seconds.
func BuildCardEvent(k *nostr.Key, spec CardSpec, createdAt int64) (*nostr.Event, error) {
	if spec.ItemID == "" {
		return nil, fmt.Errorf("sync: card event: empty item id")
	}
	tags := [][]string{
		{"d", spec.ItemID},
	}
	// CONFIDENTIAL mode drops the clear title tag (its value moves into the sealed
	// Content); plaintext mode emits it exactly where it always was (right after d).
	if spec.Enc == nil {
		tags = append(tags, []string{"title", spec.Title})
	}
	if coord := cardBoardCoord(k, spec); coord != "" {
		tags = append(tags, []string{"a", coord})
	}
	if spec.Status != "" {
		tags = append(tags, []string{"s", spec.Status})
	}
	if spec.Priority != "" {
		// priority maps to rank (ordering) AND an explicit priority tag (rd ext).
		tags = append(tags, []string{"rank", spec.Priority})
		tags = append(tags, []string{"priority", spec.Priority})
	}
	if spec.Type != "" {
		tags = append(tags, []string{"itype", spec.Type})
	}
	if spec.Assignee != "" {
		tags = append(tags, []string{"p", spec.Assignee})
	}
	for _, dep := range spec.Deps {
		if dep != "" {
			tags = append(tags, []string{"i", dep})
		}
	}
	if spec.Gate != "" {
		tags = append(tags, []string{"gate", spec.Gate})
	}
	if spec.WaitingType != "" {
		tags = append(tags, []string{"waiting_type", spec.WaitingType})
	}
	// CONFIDENTIAL mode moves waiting_on into the sealed Content (it is free text);
	// plaintext mode emits the clear tag as before.
	if spec.WaitingOn != "" && spec.Enc == nil {
		tags = append(tags, []string{"waiting_on", spec.WaitingOn})
	}
	for _, label := range spec.Labels {
		if label != "" {
			// Under tokenization (Enc.LTK set) the clear l value is an owner-keyed
			// HMAC token (equality-filterable, not readable); the plaintext label
			// rides inside the sealed Content for member-side rendering. Otherwise
			// the plaintext label is emitted exactly as before.
			if spec.Enc != nil && spec.Enc.LTK != nil {
				tags = append(tags, []string{"l", labelToken(*spec.Enc.LTK, label)})
			} else {
				tags = append(tags, []string{"l", label})
			}
		}
	}
	if spec.ETA != "" {
		tags = append(tags, []string{"eta", spec.ETA})
	}
	// Additive rd-extension tags (ready-187): humanness level, assignment scope,
	// parent/child tree edge, and due date. Each is emitted only when non-empty so
	// existing readers (and cards) are unaffected; the projection defaults a missing
	// tag to "" (backward-compatible).
	if spec.Level != "" {
		tags = append(tags, []string{"level", spec.Level})
	}
	if spec.For != "" {
		tags = append(tags, []string{"for", spec.For})
	}
	if spec.ParentID != "" {
		tags = append(tags, []string{"parent", spec.ParentID})
	}
	if spec.Due != "" {
		tags = append(tags, []string{"due", spec.Due})
	}
	// Content: plaintext mode carries the raw description; confidential mode seals
	// the {title,context,waiting_on,labels} blob and adds the clear enc/cek_epoch
	// markers. No other new clear tag is added; NO content-hash tag ever (spec §6).
	content := spec.Context
	if spec.Enc != nil {
		sealed, err := sealCardPayload(spec.Enc, spec)
		if err != nil {
			return nil, fmt.Errorf("sync: seal card content: %w", err)
		}
		content = sealed
		tags = append(tags, encMarkerTags(spec.Enc)...)
	}
	e := &nostr.Event{
		Kind:      KindCard,
		CreatedAt: createdAt,
		Tags:      tags,
		Content:   content,
	}
	if err := e.Sign(k); err != nil {
		return nil, fmt.Errorf("sync: sign card event: %w", err)
	}
	return e, nil
}

// BuildStatusEvent constructs and signs a NIP-34 status event for the card. The
// event kind encodes the NIP-34 open/resolved/closed family; the EXACT rd status
// is preserved in the "status" tag so the projection reconstructs it faithfully.
// It references the card by addressable coordinate ("a") so it survives card
// churn, and by the concrete card event id ("e") when one is supplied. reason is
// an optional close/change reason carried in the content (rd's close-with-reason).
// createdAt MUST be seconds.
func BuildStatusEvent(k *nostr.Key, itemID, rdStatus, cardEventID, reason string, createdAt int64) (*nostr.Event, error) {
	if itemID == "" {
		return nil, fmt.Errorf("sync: status event: empty item id")
	}
	if rdStatus == "" {
		return nil, fmt.Errorf("sync: status event: empty status")
	}
	tags := [][]string{
		{"a", CardCoord(k.PubKeyHex(), itemID)},
		{"d", itemID},
		{"status", rdStatus},
	}
	if cardEventID != "" {
		tags = append(tags, []string{"e", cardEventID})
	}
	e := &nostr.Event{
		Kind:      statusKindFor(rdStatus),
		CreatedAt: createdAt,
		Tags:      tags,
		Content:   reason,
	}
	if err := e.Sign(k); err != nil {
		return nil, fmt.Errorf("sync: sign status event: %w", err)
	}
	return e, nil
}

// BuildStatusEventWithIssueRoot is BuildStatusEvent PLUS an additional NIP-10
// "root"-marked "e" tag anchoring the status event to a real NIP-34 kind:1621
// issue event (ready-da7), when issueEventID is non-empty, PLUS a second "a" tag
// carrying the item's BOARD-membership coordinate (ready-7ec), when boardCoord is
// non-empty. issueEventID == "" and boardCoord == "" (the zero values) reproduce
// BuildStatusEvent's output EXACTLY — so every existing caller of BuildStatusEvent
// is untouched, and this is the only entry point that needs to change to add
// either interop anchor. The existing 30302-card anchor ("a" to CardCoord, "e" to
// cardEventID) is unchanged and still present, and remains the FIRST "a"/"e" tag
// on the event — rd's own projection (tagValue reads only the first match) keeps
// reading exactly what it read before. The issue-root "e" tag is a pure addition
// a generic NIP-34 client can use to fetch the issue and thread status onto it.
//
// WHY THE BOARD "a" TAG (ready-7ec): rd nostr sync's negentropy filter can be
// scoped to one project's board (BoardSyncFilter(boardCoord, nil)) instead of
// pulling an author's entire portfolio across every project. That filter matches
// on ANY "a" tag equal to the board coordinate (matchesFilter/tagMatches checks
// every tag with that name, not just the first) — so a CARD, which already
// carries the board coordinate as its "a" tag, matches. A NIP-34 status event
// previously carried ONLY the card's OWN coordinate (30302:<signer>:<itemID>),
// never the board's (30301:<owner>:<boardD>), so a board-scoped filter silently
// matched cards but missed every status event, breaking NIP-34 status
// convergence for a pinned-board sync. Adding the board coordinate as a SECOND,
// purely additive "a" tag — the same one the card already carries via
// cardBoardCoord — fixes this without touching the card-coordinate anchor at
// tag position 0 that everything else (rd's own projection, the ready-da7 issue
// anchor, the ready-d65 migration parity) already depends on.
func BuildStatusEventWithIssueRoot(k *nostr.Key, itemID, rdStatus, cardEventID, issueEventID, boardCoord, reason string, createdAt int64, env *Envelope) (*nostr.Event, error) {
	e, err := BuildStatusEvent(k, itemID, rdStatus, cardEventID, reason, createdAt)
	if err != nil {
		return nil, err
	}
	changed := false
	// CONFIDENTIAL mode: seal the {reason} blob into Content and add the clear
	// enc/cek_epoch markers. BuildStatusEvent set the plaintext reason in Content
	// (in-memory only); it is overwritten here before the re-sign below, so the
	// plaintext reason is never signed or published. This is the single status-event
	// path the live confidential write flow uses (nostroutbound Publisher).
	if env != nil {
		sealed, err := sealStatusPayload(env, reason)
		if err != nil {
			return nil, fmt.Errorf("sync: seal status content: %w", err)
		}
		e.Content = sealed
		e.Tags = append(e.Tags, encMarkerTags(env)...)
		changed = true
	}
	if issueEventID != "" {
		// NIP-10 marked "e" tag: ["e", <event-id>, <relay-hint>, "root"]. The relay
		// hint is left empty (rd doesn't track per-event relay provenance); readers
		// fall back to their own relay set, which is standard and harmless.
		e.Tags = append(e.Tags, []string{"e", issueEventID, "", "root"})
		changed = true
	}
	if boardCoord != "" {
		e.Tags = append(e.Tags, []string{"a", boardCoord})
		changed = true
	}
	if !changed {
		return e, nil
	}
	// Tags changed -> id/sig must be recomputed over the new canonical form.
	if err := e.Sign(k); err != nil {
		return nil, fmt.Errorf("sync: sign status event (issue root/board): %w", err)
	}
	return e, nil
}

// BuildIssueEvent constructs and signs the NIP-34 kind:1621 "issue" event that
// anchors an rd item for generic issue-tracker interop (ready-da7). It carries:
//   - "d": the rd item id. This is an rd-EXTENSION lookup tag, not a NIP-01
//     addressable-event "d" tag (kind 1621 is a regular, non-replaceable event) —
//     an unknown extra tag is harmless/ignored by generic clients, and it is what
//     lets rd re-find the ALREADY-published issue event for an item so it never
//     publishes a second one on a later status change or republish (see
//     FindIssueEventID / Publisher.ensureIssueEvent in nostroutbound.go).
//   - "subject": the issue title, the standard NIP-34 issue/patch title tag.
//
// Content carries the item's context/description at issue-creation time.
//
// rd does not model NIP-34 repositories (kind 30617) — an rd item lives on rd's
// OWN NIP-100 board (kind 30301), a distinct concept — so this issue event
// deliberately carries no repository "a" tag. That is a documented scope
// decision, not an ambiguity in the interop anchor this change adds: the
// 1630-1632 -> 1621 "root" reference (BuildStatusEventWithIssueRoot) is the
// standard NIP-34 pattern regardless of whether the issue also belongs to a
// declared repository, and is sufficient for a generic client to fetch the
// issue and associate status events with it (the ready-da7 proof requirement).
func BuildIssueEvent(k *nostr.Key, spec CardSpec, createdAt int64) (*nostr.Event, error) {
	if spec.ItemID == "" {
		return nil, fmt.Errorf("sync: issue event: empty item id")
	}
	tags := [][]string{
		{"d", spec.ItemID},
		{"subject", spec.Title},
	}
	e := &nostr.Event{
		Kind:      KindIssue,
		CreatedAt: createdAt,
		Tags:      tags,
		Content:   spec.Context,
	}
	if err := e.Sign(k); err != nil {
		return nil, fmt.Errorf("sync: sign issue event: %w", err)
	}
	return e, nil
}

// FindIssueEventID returns the event id of itemID's already-published NIP-34
// kind:1621 issue-root event, or "" if none has been published yet. rd
// publishes AT MOST ONE issue event per item (ready-da7): the first match by
// its "d" lookup tag is authoritative, and callers only ever add a new one when
// this returns "".
func FindIssueEventID(events []*nostr.Event, itemID string) string {
	for _, e := range events {
		if e.Kind == KindIssue && tagValue(e, "d") == itemID {
			return e.ID
		}
	}
	return ""
}

// tagValue returns the first value of the first tag whose name matches, or "".
func tagValue(e *nostr.Event, name string) string {
	for _, t := range e.Tags {
		if len(t) >= 2 && t[0] == name {
			return t[1]
		}
	}
	return ""
}

// tagValues returns the values of every tag whose name matches, in tag order.
// Used for repeatable tags (e.g. "i" — one per blocking dependency).
func tagValues(e *nostr.Event, name string) []string {
	var out []string
	for _, t := range e.Tags {
		if len(t) >= 2 && t[0] == name {
			out = append(out, t[1])
		}
	}
	return out
}

// isStatusKind reports whether a kind is one of the NIP-34 status kinds.
func isStatusKind(kind int) bool {
	return kind >= KindStatusOpen && kind <= KindStatusDraft
}

// DriftScope returns the CAUSAL-CHAIN key an event belongs to, for per-scope
// monotonic created_at stamping (ready-be1). Latest-wins projection orders each
// item card / status chain and each role-grant slot INDEPENDENTLY, so intent
// ordering only ever needs to hold WITHIN one such chain — never log-wide. Scoping
// the monotonic "newest+1" bump to the chain (instead of the whole log) bounds the
// future-drift of any single card/grant to the number of same-second writes TO THAT
// SAME chain (create+claim+a few edits — a couple of seconds), so an unrelated burst
// (rd engage over N items, a grant burst) can no longer inflate a fresh card/grant's
// created_at arbitrarily into the future and beat a genuinely-later cross-machine
// edit/revoke (the ready-be1 lost-update / lost-revoke). Returns "" for an event
// with no rd causal chain (it then matches no scope).
func DriftScope(e *nostr.Event) string {
	if e == nil {
		return ""
	}
	if e.Kind == KindRoleGrant {
		if d := tagValue(e, "d"); d != "" {
			return "grant:" + d
		}
		return ""
	}
	if e.Kind == KindBoard {
		if d := tagValue(e, "d"); d != "" {
			return "board:" + d
		}
		return ""
	}
	if id := itemIDForEvent(e); id != "" {
		return "item:" + id
	}
	return ""
}

// ItemDriftScope is the DriftScope key for an item's card/status/issue chain. A
// caller about to publish a mutation for itemID passes this to nostrNextCreatedAt so
// the monotonic bump considers only THIS item's prior events.
func ItemDriftScope(itemID string) string { return "item:" + itemID }

// GrantDriftScope is the DriftScope key for a role-grant's addressable (board,
// grantee) slot — matching DriftScope of a 39301 event whose "d" is roleGrantD(
// boardD, grantee). A grant and its later revoke share this scope, so a revoke
// stamps strictly after the grant it supersedes without log-wide drift.
func GrantDriftScope(boardD, grantee string) string { return "grant:" + roleGrantD(boardD, grantee) }

// itemIDForEvent extracts the rd item ID an event pertains to. Cards carry it in
// "d"; status events carry it in "d" and/or the "a" coordinate. Returns "" when
// the event is not an rd item event (e.g. a board).
func itemIDForEvent(e *nostr.Event) string {
	if e.Kind == KindCard {
		return tagValue(e, "d")
	}
	if isStatusKind(e.Kind) {
		if d := tagValue(e, "d"); d != "" {
			return d
		}
		// Fall back to the "a" coordinate: "30302:<pubkey>:<itemID>".
		if a := tagValue(e, "a"); a != "" {
			parts := strings.SplitN(a, ":", 3)
			if len(parts) == 3 {
				return parts[2]
			}
		}
	}
	return ""
}
