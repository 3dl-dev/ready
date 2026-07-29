package main

// Confidential-by-default board wiring (epic ready-216, ready-deb).
//
// A board initialized by `rd init` is CONFIDENTIAL by default (opt out with
// --public): the owner's first write mints a per-board CEK + LTK and publishes an
// owner SELF-grant carrying them (NIP-44-wrapped to the owner's own key) so the
// key material is recoverable from the replicated event log via the identity key —
// $RD_HOME holds no separate secret. Every card/status event then seals its free
// text into event.Content while routing tags stay clear and labels are tokenized.
// Members receive the CEK+LTK when the owner `rd grant`s them; `rd revoke`/`rd
// kill` rotates the epoch (forward secrecy).

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/rdconfig"
	rdSync "github.com/3dl-dev/ready/pkg/sync"
)

// cutoverCreatedAt returns a timestamp strictly greater than EVERY event already
// in the log (and >= now). The CEK self-grant that establishes a board's
// confidential cutover must sort AFTER every pre-existing plaintext card, so those
// cards are genuinely pre-cutover (grandfathered) — otherwise, when a card and the
// enabling self-grant land in the same wall-clock second, the strict
// `created_at < cutover` grandfather check would quarantine the pre-existing card.
// Unlike nostrNextCreatedAt this scans the WHOLE log, not a single drift scope.
func cutoverCreatedAt(log *rdSync.NostrLog) int64 {
	now := time.Now().Unix()
	events, err := log.ReadAll()
	if err != nil {
		return now
	}
	var max int64
	for _, e := range events {
		if e.CreatedAt > max {
			max = e.CreatedAt
		}
	}
	if max+1 > now {
		return max + 1
	}
	return now
}

// boardIsConfidential reports whether the project at dir is confidential.
// Confidentiality is the DEFAULT (epic ready-216): a board is confidential UNLESS
// its config sets Public. A missing config, or one omitting the flag, is
// confidential. (On a config read error we fail SAFE — confidential — rather than
// silently writing plaintext to a board that should be sealed.)
func boardIsConfidential(dir string) bool {
	cfg, err := rdconfig.LoadSyncConfig(dir)
	if err != nil {
		return true
	}
	return !cfg.Public
}

// envelopeFromKeyring builds a sealing Envelope from a reader's keyring for the
// board's current epoch, or nil if the reader holds no CEK.
func envelopeFromKeyring(kr *rdSync.BoardKeyring, coord string) *rdSync.Envelope {
	epoch, cek, ok := kr.CurrentEpoch(coord)
	if !ok {
		return nil
	}
	env := &rdSync.Envelope{CEK: cek, Epoch: epoch}
	if ltk, ok := kr.LTK(coord); ok {
		l := ltk
		env.LTK = &l
	}
	return env
}

// boardConfidentialEnvelope returns the Envelope a write to this board must seal
// under, or nil for a plaintext board. It BOOTSTRAPS confidentiality on the
// owner's first write (mint CEK+LTK, publish the owner self-grant), and errors if
// the board is confidential but the writer holds no key (so a plaintext card is
// never published where the fold gate would quarantine it — and a placeholder is
// never re-sealed over real content).
func boardConfidentialEnvelope(dir string, pub *rdSync.Publisher, boardAuthor, boardD string) (*rdSync.Envelope, error) {
	if !boardIsConfidential(dir) {
		return nil, nil
	}
	// Confidentiality is keyed to a PINNED board coordinate (the CEK is per-board).
	// A project with no pinned board (legacy RD_NOSTR without a BP-3 pin) has no
	// board to seal to → plaintext. A real `rd init` always pins, so this only
	// exempts the unpinned legacy/edge case.
	if nostrPinnedBoard(dir) == "" {
		return nil, nil
	}
	coord := rdSync.BoardCoord(boardAuthor, boardD)
	events, err := pub.Log.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("reading log for board keys: %w", err)
	}
	kr := rdSync.DeriveBoardKeyring(events, pub.Key, boardAuthor, boardD)
	if env := envelopeFromKeyring(kr, coord); env != nil {
		return env, nil // already confidential and I hold the current key
	}

	signer := pub.Key.PubKeyHex()
	// Edge #5 self-heal (ready-bd0): I hold no readable CEK, yet a valid owner-signed
	// grant carrying my read key may already exist on a relay and simply not have
	// reached the local log. Before erroring "ask the owner to grant your pubkey" —
	// which tells me to do what the owner already did — do ONE targeted reconcile of
	// owner-signed 39301 grants addressed to me, re-read the log, and retry the key
	// derivation. Bounded to a single fetch (no loop): if no valid grant exists, the
	// original error below still fires.
	//
	// This runs for the OWNER TOO (ready-889). The owner reaching this point on a
	// board that is already confidential means its local log lost the self-grant — a
	// fresh clone, a rebuilt log — and the branch below would otherwise mint a SECOND
	// key at epoch 1, silently orphaning every card sealed under the first. That is
	// not hypothetical: it is what permanently destroyed three cards on the ready
	// board. Asking the relay first is what makes the difference between recovering
	// the board key and replacing it.
	env, healed := reconcileSelfGrantEnvelope(dir, pub, boardAuthor, boardD, coord)
	if healed {
		return env, nil
	}
	// The reconcile may have merged the owner self-grant (which establishes the
	// board cutover) even when no key-bearing grant for me arrived; re-read so the
	// cutover/error decision below reflects anything the fetch pulled in.
	if evs, rerr := pub.Log.ReadAll(); rerr == nil {
		events = evs
		kr = rdSync.DeriveBoardKeyring(events, pub.Key, boardAuthor, boardD)
	}
	if _, alreadyConfidential := kr.Cutover(coord); alreadyConfidential {
		// Board is confidential but I have no readable CEK.
		if signer != boardAuthor {
			return nil, fmt.Errorf("board %s is confidential and you hold no read key — ask the owner to `rd grant` your pubkey %s", coord, signer)
		}
		return nil, fmt.Errorf("board %s is confidential but the owner CEK could not be recovered from the log", coord)
	}
	// Not yet confidential. Only the OWNER bootstraps it; a non-owner writing before
	// the owner's first write stays plaintext (the board is not confidential yet).
	if signer != boardAuthor {
		return nil, nil
	}
	// FAIL CLOSED ON A LOST KEY (ready-889). "No cutover in the local log" is only
	// evidence that the board is NEW if the log is complete. If this board already
	// has SEALED CARDS, it is not a plaintext board about to become confidential —
	// it is a confidential board whose grant did not survive into this log, and
	// minting here would install a second epoch-1 key, orphaning every card sealed
	// under the first. That is precisely how three cards on the ready board were
	// destroyed. The check is local, so it holds offline too — refusing purely on
	// relay unreachability would break rd's offline-first write path for the common
	// case (a genuinely new board) to guard the rare one.
	if rdSync.HasConfidentialCard(events, coord) {
		return nil, fmt.Errorf("refusing to bootstrap a confidential key for board %s: the board already has sealed cards but no readable CEK grant in this log, so its key was LOST, not absent. "+
			"Minting a new epoch-1 key here would permanently orphan every card sealed under the old one. Recover the grant from a relay (rd relay repair) or from a machine that still holds it", coord)
	}
	cek, err := rdSync.MintKey()
	if err != nil {
		return nil, err
	}
	ltk, err := rdSync.MintKey()
	if err != nil {
		return nil, err
	}
	if _, err := publishOwnerCEKSelfGrant(pub, boardAuthor, boardD, cek, ltk, 1); err != nil {
		return nil, fmt.Errorf("bootstrapping confidential board key: %w", err)
	}
	// Wrap the CEK to every EXISTING member too, so flipping a board that already
	// has members to confidential does not lock them out of their own board. A key
	// already REVOKED on the plaintext board is withheld — it must not gain a read
	// key by the board becoming confidential.
	members, _ := rotationMembership(events, boardAuthor, boardD, "")
	if _, err := wrapEpochToMembers(pub, boardAuthor, boardD, cek, ltk, 1, members); err != nil {
		return nil, fmt.Errorf("wrapping confidential board key to members: %w", err)
	}
	l := ltk
	return &rdSync.Envelope{CEK: cek, Epoch: 1, LTK: &l}, nil
}

// reconcileSelfGrantEnvelope is the edge #5 self-heal (ready-bd0): on the
// confidential-write path, when the local log yields no read key, it does ONE targeted
// reconcile of owner-signed 39301 grants addressed to THIS pubkey (the read key rides
// in the grant — pkg/sync/keydist.go), merges them into the local log, and returns the
// sealing Envelope if a valid grant now yields the current-epoch CEK. It is bounded to
// a single relay fetch (no recursion/loop), so a genuinely-absent grant returns
// healed=false and the caller's original "ask the owner to grant" error still fires. On
// a local-only project (no read relays) it is a no-op returning healed=false. Security:
// rdSync.DeriveBoardKeyring re-checks that any usable key came from a grant signed by
// the board OWNER, addressed to (and openable by) this key — a hostile relay cannot
// inject a usable key here.
func reconcileSelfGrantEnvelope(dir string, pub *rdSync.Publisher, boardAuthor, boardD, coord string) (*rdSync.Envelope, bool) {
	relays := nostrReadRelays()
	if len(relays) == 0 {
		return nil, false // local-only: nothing to fetch, the original error stands
	}
	self := pub.Key.PubKeyHex()
	if _, err := rdSync.ReconcileSelfGrants(context.Background(), relays, pub.Log, coord, self, nostrTrustSet(dir, self), autoReconcileTimeout); err != nil {
		return nil, false // relay error is non-fatal: fall back to the original error
	}
	events, err := pub.Log.ReadAll()
	if err != nil {
		return nil, false
	}
	kr := rdSync.DeriveBoardKeyring(events, pub.Key, boardAuthor, boardD)
	if e := envelopeFromKeyring(kr, coord); e != nil {
		return e, true
	}
	return nil, false
}

// rotationMembership splits the board's kind-39301 grant-holders into the keys
// that MUST receive a new epoch CEK and the keys that must NOT.
//
// receive = every key holding a LIVE (non-revoked) cap-valid grant, minus the
// board owner (self-granted separately by publishOwnerCEKSelfGrant) and minus
// `exclude` (the key being revoked in the very act that triggered the rotation —
// its revoked grant may not have been derived yet by the caller's snapshot).
// withheld = every other grant-holder: each revoked key, plus `exclude`.
//
// THE REVOKED FILTER IS LOAD-BEARING AND IS NOT THE SAME AS `exclude`.
// DeriveLevels/DeriveReadTrust deliberately KEEP a revoked key in the membership
// map so its PAST events stay admissible (prospective revocation, rolegrant.go
// DeriveReadTrust). Wrapping a new epoch to "everyone in that map except the one
// key being revoked right now" therefore hands the fresh CEK straight back to
// every key revoked in an EARLIER rotation: forward secrecy would hold for
// exactly one revocation and then silently unwind on the next one.
func rotationMembership(events []*nostr.Event, boardAuthor, boardD, exclude string) (receive []rdSync.GrantHolder, withheld []rdSync.GrantHolder) {
	for pk, h := range rdSync.DeriveGrantHolders(events, boardAuthor, boardD) {
		if pk == boardAuthor {
			continue // the owner is self-granted, not wrapped as a member
		}
		if h.Level == rdSync.LevelRevoked || pk == exclude {
			withheld = append(withheld, h)
			continue
		}
		receive = append(receive, h)
	}
	sortHolders(receive)
	sortHolders(withheld)
	return receive, withheld
}

// sortHolders orders holders by pubkey so every rotation reports (and publishes)
// its members in a stable, diffable order.
func sortHolders(hs []rdSync.GrantHolder) {
	sort.Slice(hs, func(i, j int) bool { return hs[i].Pubkey < hs[j].Pubkey })
}

// holderPubkeys projects a holder list to its pubkeys, for output and tests.
func holderPubkeys(hs []rdSync.GrantHolder) []string {
	out := make([]string, 0, len(hs))
	for _, h := range hs {
		out = append(out, h.Pubkey)
	}
	return out
}

// wrapEpochToMembers publishes one owner-signed grant per member, each carrying
// the (cek, ltk, epoch) wrapped to that member, and returns the signed events it
// published (so a caller can read them back off a relay).
//
// Each re-issued grant carries the member's CURRENT role and label, not a
// hardcoded contributor/epoch-label pair: kind-39301 is addressable on
// (board, grantee), so this grant REPLACES the member's existing one, and
// re-issuing at a fixed role would demote every maintainer on every rotation.
// A member with no human label of its own gets the epoch label instead of an
// empty one.
//
// Callers pass the list from rotationMembership, which is what enforces the
// revoked-key exclusion — this function wraps to exactly whom it is told to.
func wrapEpochToMembers(pub *rdSync.Publisher, boardAuthor, boardD string, cek, ltk [32]byte, epoch int, members []rdSync.GrantHolder) ([]*nostr.Event, error) {
	var published []*nostr.Event
	for _, member := range members {
		wCEK, werr := rdSync.WrapKey(pub.Key, member.Pubkey, cek)
		if werr != nil {
			return published, werr
		}
		wLTK, werr := rdSync.WrapKey(pub.Key, member.Pubkey, ltk)
		if werr != nil {
			return published, werr
		}
		role := member.Role
		if role == "" {
			role = rdSync.RoleContributor
		}
		label := member.Label
		if label == "" {
			label = "confidential-board key (epoch " + strconv.Itoa(epoch) + ")"
		}
		spec := rdSync.RoleGrantSpec{
			BoardD: boardD, BoardAuthor: boardAuthor, Grantee: member.Pubkey, Role: role,
			Label:      label,
			WrappedCEK: wCEK, CEKEpoch: epoch, WrappedLTK: wLTK,
		}
		ev, berr := rdSync.BuildRoleGrantEvent(pub.Key, spec, nostrNextCreatedAt(pub.Log, rdSync.GrantDriftScope(boardD, member.Pubkey)))
		if berr != nil {
			return published, berr
		}
		if _, perr := pub.PublishEvents(context.Background(), []*nostr.Event{ev}); perr != nil {
			return published, perr
		}
		published = append(published, ev)
	}
	return published, nil
}

// setCardEnvelope seals card for the board's confidentiality mode (or leaves it
// plaintext), bootstrapping the owner CEK on first write. Callers already hold
// dir/pub/boardAuthor/boardD.
func setCardEnvelope(dir string, pub *rdSync.Publisher, boardAuthor, boardD string, card *rdSync.CardSpec) error {
	env, err := boardConfidentialEnvelope(dir, pub, boardAuthor, boardD)
	if err != nil {
		return err
	}
	card.Enc = env
	return nil
}

// publishOwnerCEKSelfGrant publishes a kind-39301 grant p-tagged to the OWNER
// itself, carrying the epoch CEK + the LTK NIP-44-wrapped to the owner's own key.
// This makes the key material recoverable from the log by anyone holding the
// identity key — the source of truth is the replicated log, not $RD_HOME.
func publishOwnerCEKSelfGrant(pub *rdSync.Publisher, boardAuthor, boardD string, cek, ltk [32]byte, epoch int) (*nostr.Event, error) {
	owner := pub.Key.PubKeyHex()
	wCEK, err := rdSync.WrapKey(pub.Key, owner, cek)
	if err != nil {
		return nil, err
	}
	wLTK, err := rdSync.WrapKey(pub.Key, owner, ltk)
	if err != nil {
		return nil, err
	}
	spec := rdSync.RoleGrantSpec{
		BoardD: boardD, BoardAuthor: boardAuthor, Grantee: owner, Role: rdSync.RoleOwner,
		Label:      "confidential-board key (epoch " + strconv.Itoa(epoch) + ")",
		WrappedCEK: wCEK, CEKEpoch: epoch, WrappedLTK: wLTK,
	}
	// Stamp the CEK self-grant strictly after every pre-existing event so it
	// establishes a cutover that grandfathers all prior plaintext cards.
	ev, err := rdSync.BuildRoleGrantEvent(pub.Key, spec, cutoverCreatedAt(pub.Log))
	if err != nil {
		return nil, err
	}
	if _, err := pub.PublishEvents(context.Background(), []*nostr.Event{ev}); err != nil {
		return nil, err
	}
	return ev, nil
}

// boardReadKeyring derives the reader's confidential-board key material from the
// event log, for wiring into ProjectOptions.{Decryptor,EncryptedBoards}. Returns
// nil when the project has no pinned board (nothing to key on) — a nil keyring is
// safe in ProjectOptions (Decryptor/EncryptedBoards no-op).
func boardReadKeyring(dir string, reader *nostr.Key, events []*nostr.Event) *rdSync.BoardKeyring {
	pin := nostrPinnedBoard(dir)
	if pin == "" {
		return nil
	}
	owner, boardD, ok := rdSync.ParseBoardCoord(pin)
	if !ok {
		return nil
	}
	return rdSync.DeriveBoardKeyring(events, reader, owner, boardD)
}

// confidentialGrantKeys returns the wrapped CEK/epoch/LTK to embed in a role grant
// to `grantee`, or empty strings when the board is plaintext, the role does not
// confer read access, or the signer is not the owner (only the owner-signed CEK is
// honored by DeriveBoardKeyring). pub.Key must be the granting signer.
func confidentialGrantKeys(dir string, pub *rdSync.Publisher, boardAuthor, boardD, grantee, role string) (wCEK string, epoch int, wLTK string, err error) {
	if !boardIsConfidential(dir) || role == rdSync.RoleRevoked {
		return "", 0, "", nil
	}
	if pub.Key.PubKeyHex() != boardAuthor {
		return "", 0, "", nil // only the owner distributes the board CEK
	}
	events, err := pub.Log.ReadAll()
	if err != nil {
		return "", 0, "", err
	}
	coord := rdSync.BoardCoord(boardAuthor, boardD)
	kr := rdSync.DeriveBoardKeyring(events, pub.Key, boardAuthor, boardD)
	ep, cek, ok := kr.CurrentEpoch(coord)
	if !ok {
		// Board marked confidential but not yet bootstrapped (no owner write yet):
		// nothing to wrap. The grantee gets the CEK once the owner writes + regrants.
		return "", 0, "", nil
	}
	wCEK, err = rdSync.WrapKey(pub.Key, grantee, cek)
	if err != nil {
		return "", 0, "", err
	}
	if ltk, ok := kr.LTK(coord); ok {
		if wLTK, err = rdSync.WrapKey(pub.Key, grantee, ltk); err != nil {
			return "", 0, "", err
		}
	}
	return wCEK, ep, wLTK, nil
}

// epochRotationPlan is what a CEK rotation WOULD do, computed from the local log
// with nothing published. `rd confidential rotate --dry-run` prints it, and
// rotateBoardEpoch executes it, so the preview and the act cannot disagree.
type epochRotationPlan struct {
	// Coord is the board the rotation applies to.
	Coord string
	// OldEpoch is the highest CEK epoch the owner currently holds; NewEpoch is
	// OldEpoch+1, the epoch future writes will seal under.
	OldEpoch int
	NewEpoch int
	// Members are the grant-holders that WILL receive the new epoch, sorted by
	// pubkey (the owner is excluded — it is self-granted, not wrapped).
	Members []rdSync.GrantHolder
	// Withheld are the grant-holders that will NOT: every revoked key, plus the
	// key being revoked in the act that triggered this rotation.
	Withheld []rdSync.GrantHolder
	// ltk is the board's Label Token Key. It is STABLE across CEK epochs by
	// design (ready-c83 ruling (b)): label tags are HMAC-SHA256(LTK, label), so
	// re-keying the LTK would change every future token and silently split label
	// search across the rotation boundary. Rotation therefore re-wraps the
	// EXISTING LTK alongside the new CEK.
	ltk [32]byte
}

// planBoardRotation computes the rotation for the pinned confidential board from
// the local log alone — no relay, no writes. It errors (rather than no-opping)
// for every reason a rotation cannot happen, so the CLI can say which one.
func planBoardRotation(dir string, pub *rdSync.Publisher, boardAuthor, boardD, exclude string) (*epochRotationPlan, error) {
	coord := rdSync.BoardCoord(boardAuthor, boardD)
	if !boardIsConfidential(dir) {
		return nil, fmt.Errorf("board %s is PUBLIC — there is no content-encryption key to rotate", coord)
	}
	signer := pub.Key.PubKeyHex()
	if signer != boardAuthor {
		return nil, fmt.Errorf("only the board OWNER can rotate the key: board %s is owned by %s, this key is %s", coord, boardAuthor, signer)
	}
	events, err := pub.Log.ReadAll()
	if err != nil {
		return nil, err
	}
	kr := rdSync.DeriveBoardKeyring(events, pub.Key, boardAuthor, boardD)
	curEpoch, _, ok := kr.CurrentEpoch(coord)
	if !ok {
		return nil, fmt.Errorf("board %s has no CEK to rotate — it is marked confidential but not yet bootstrapped (no owner write yet); run `rd confidential enable`", coord)
	}
	ltk, hasLTK := kr.LTK(coord)
	if !hasLTK {
		// Rotating without the LTK would have to mint a new one, which changes
		// every future label token and orphans label search on existing cards.
		// Refuse rather than half-rotate.
		return nil, fmt.Errorf("cannot rotate epoch: board %s LTK not recoverable from the log", coord)
	}
	members, withheld := rotationMembership(events, boardAuthor, boardD, exclude)
	return &epochRotationPlan{
		Coord: coord, OldEpoch: curEpoch, NewEpoch: curEpoch + 1,
		Members: members, Withheld: withheld, ltk: ltk,
	}, nil
}

// rotateBoardEpoch executes a plan: it mints a FRESH random CEK (never derived
// from the old one — a rotation whose new key is a function of the compromised
// one is no remedy at all), publishes the owner self-grant for the new epoch so
// the key stays recoverable from the log, and re-wraps that CEK plus the stable
// LTK to every member in the plan.
//
// It does NOT touch a single existing card. Cards sealed under epochs <= OldEpoch
// keep their ciphertext, their cek_epoch marker and their event ids, so every
// holder of those epochs — including the owner, every current member, and any
// leaked key or previously-minted `rd board --with-key` link — still reads them.
// Rotation closes the FUTURE, not the past; re-sealing history would rewrite
// thousands of signed events and would make a rotation more destructive than the
// leak it answers.
//
// It returns the signed grants it published so the caller can read them back off
// a named relay (rd's own publish report cannot prove any particular relay took
// them — see pkg/sync/VerifyEventsOnRelay).
func rotateBoardEpoch(pub *rdSync.Publisher, boardAuthor, boardD string, plan *epochRotationPlan) ([]*nostr.Event, error) {
	newCEK, err := rdSync.MintKey()
	if err != nil {
		return nil, err
	}
	selfGrant, err := publishOwnerCEKSelfGrant(pub, boardAuthor, boardD, newCEK, plan.ltk, plan.NewEpoch)
	if err != nil {
		return nil, err
	}
	published := []*nostr.Event{selfGrant}
	memberGrants, err := wrapEpochToMembers(pub, boardAuthor, boardD, newCEK, plan.ltk, plan.NewEpoch, plan.Members)
	published = append(published, memberGrants...)
	if err != nil {
		// Report what DID land: those members hold the new epoch and the rest do
		// not, and the caller needs the ids to read back and to re-run against.
		return published, err
	}
	return published, nil
}

// rekeyBoardOnRevoke mints a NEW epoch CEK, publishes the owner self-grant for it,
// and re-wraps it (with the stable LTK) to every REMAINING non-revoked member —
// excluding revokedPubkey — so cards authored after the revoke are unreadable by
// the revoked key (forward secrecy). No-op on a plaintext board, a non-owner
// signer, or a board whose CEK was never bootstrapped: a revoke must still
// succeed on those, so the conditions planBoardRotation reports as errors are
// checked here first and treated as "nothing to rotate".
func rekeyBoardOnRevoke(dir string, pub *rdSync.Publisher, boardAuthor, boardD, revokedPubkey string) error {
	if !boardIsConfidential(dir) || pub.Key.PubKeyHex() != boardAuthor {
		return nil
	}
	events, err := pub.Log.ReadAll()
	if err != nil {
		return err
	}
	coord := rdSync.BoardCoord(boardAuthor, boardD)
	kr := rdSync.DeriveBoardKeyring(events, pub.Key, boardAuthor, boardD)
	if _, _, ok := kr.CurrentEpoch(coord); !ok {
		return nil // board not yet bootstrapped — nothing to rotate
	}
	plan, err := planBoardRotation(dir, pub, boardAuthor, boardD, revokedPubkey)
	if err != nil {
		return err
	}
	_, err = rotateBoardEpoch(pub, boardAuthor, boardD, plan)
	return err
}
