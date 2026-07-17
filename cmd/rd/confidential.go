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
	// Edge #5 self-heal (ready-bd0): I (a non-owner) hold no readable CEK, yet a valid
	// owner-signed grant carrying my read key may already exist on a relay and simply
	// not have reached the local log. Before erroring "ask the owner to grant your
	// pubkey" — which tells me to do what the owner already did — do ONE targeted
	// reconcile of owner-signed 39301 grants addressed to me, re-read the log, and
	// retry the key derivation. Bounded to a single fetch (no loop): if no valid grant
	// exists, the original error below still fires. Only for a non-owner: the owner
	// bootstraps its own key and never needs to fetch a grant for itself.
	if signer != boardAuthor {
		if env, healed := reconcileSelfGrantEnvelope(dir, pub, boardAuthor, boardD, coord); healed {
			return env, nil
		}
		// The reconcile may have merged the owner self-grant (which establishes the
		// board cutover) even when no key-bearing grant for me arrived; re-read so the
		// cutover/error decision below reflects anything the fetch pulled in.
		if evs, rerr := pub.Log.ReadAll(); rerr == nil {
			events = evs
			kr = rdSync.DeriveBoardKeyring(events, pub.Key, boardAuthor, boardD)
		}
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
	cek, err := rdSync.MintKey()
	if err != nil {
		return nil, err
	}
	ltk, err := rdSync.MintKey()
	if err != nil {
		return nil, err
	}
	if err := publishOwnerCEKSelfGrant(pub, boardAuthor, boardD, cek, ltk, 1); err != nil {
		return nil, fmt.Errorf("bootstrapping confidential board key: %w", err)
	}
	// Wrap the CEK to every EXISTING member too, so flipping a board that already
	// has members to confidential does not lock them out of their own board.
	if err := wrapEpochToMembers(pub, boardAuthor, boardD, cek, ltk, 1, ""); err != nil {
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
	if env := envelopeFromKeyring(kr, coord); env != nil {
		return env, true
	}
	return nil, false
}

// wrapEpochToMembers publishes an owner-signed grant carrying the (cek, ltk, epoch)
// wrapped to each current read-trusted member of the board EXCEPT the owner (which
// is self-granted separately) and any excluded pubkey (e.g. a just-revoked member).
// Shared by bootstrap (so an existing board's members keep access when it becomes
// confidential) and by revoke rekey (so remaining members get the new epoch).
func wrapEpochToMembers(pub *rdSync.Publisher, boardAuthor, boardD string, cek, ltk [32]byte, epoch int, exclude string) error {
	events, err := pub.Log.ReadAll()
	if err != nil {
		return err
	}
	for member := range rdSync.DeriveReadTrust(events, boardAuthor, boardD) {
		if member == boardAuthor || member == exclude {
			continue
		}
		wCEK, werr := rdSync.WrapKey(pub.Key, member, cek)
		if werr != nil {
			return werr
		}
		wLTK, werr := rdSync.WrapKey(pub.Key, member, ltk)
		if werr != nil {
			return werr
		}
		spec := rdSync.RoleGrantSpec{
			BoardD: boardD, BoardAuthor: boardAuthor, Grantee: member, Role: rdSync.RoleContributor,
			Label:      "confidential-board key (epoch " + strconv.Itoa(epoch) + ")",
			WrappedCEK: wCEK, CEKEpoch: epoch, WrappedLTK: wLTK,
		}
		ev, berr := rdSync.BuildRoleGrantEvent(pub.Key, spec, nostrNextCreatedAt(pub.Log, rdSync.GrantDriftScope(boardD, member)))
		if berr != nil {
			return berr
		}
		if _, perr := pub.PublishEvents(context.Background(), []*nostr.Event{ev}); perr != nil {
			return perr
		}
	}
	return nil
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
func publishOwnerCEKSelfGrant(pub *rdSync.Publisher, boardAuthor, boardD string, cek, ltk [32]byte, epoch int) error {
	owner := pub.Key.PubKeyHex()
	wCEK, err := rdSync.WrapKey(pub.Key, owner, cek)
	if err != nil {
		return err
	}
	wLTK, err := rdSync.WrapKey(pub.Key, owner, ltk)
	if err != nil {
		return err
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
		return err
	}
	_, err = pub.PublishEvents(context.Background(), []*nostr.Event{ev})
	return err
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

// rekeyBoardOnRevoke mints a NEW epoch CEK, publishes the owner self-grant for it,
// and re-wraps it (with the stable LTK) to every REMAINING read-trusted member —
// excluding revokedPubkey — so cards authored after the revoke are unreadable by
// the revoked key (forward secrecy). No-op on a plaintext board or when the signer
// is not the owner.
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
	curEpoch, _, ok := kr.CurrentEpoch(coord)
	if !ok {
		return nil // board not yet bootstrapped — nothing to rotate
	}
	ltk, hasLTK := kr.LTK(coord)
	if !hasLTK {
		return fmt.Errorf("cannot rotate epoch: board %s LTK not recoverable", coord)
	}
	newEpoch := curEpoch + 1
	newCEK, err := rdSync.MintKey()
	if err != nil {
		return err
	}
	// Owner self-grant for the new epoch (keeps the CEK recoverable from the log).
	if err := publishOwnerCEKSelfGrant(pub, boardAuthor, boardD, newCEK, ltk, newEpoch); err != nil {
		return err
	}
	// Re-wrap the new epoch CEK (+ the stable LTK) to every remaining read-trusted
	// member, excluding the just-revoked key (the owner is self-granted above).
	return wrapEpochToMembers(pub, boardAuthor, boardD, newCEK, ltk, newEpoch, revokedPubkey)
}
