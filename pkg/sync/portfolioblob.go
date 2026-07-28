package sync

// The `keys=` portfolio blob (ready-4d9) — the wire format that lets ONE board
// link carry the read keys for EVERY board a key can read.
//
// It lives in pkg/sync rather than in cmd/rd for one reason: there must be
// exactly ONE encoder. The cross-language conformance vectors
// (web/board/testdata/portfolio-key-vectors.json) are produced by a generator
// that is not package main, and a generator with its own copy of the encoding
// would pin whatever IT does rather than what `rd` emits.
//
// WHY BINARY, AND NOT A LONGER cek= LIST. ready-df0's grammar,
// cek=<epoch>:<64-hex>[,...], extends naturally to board scoping — and that
// extension fails in the one way this link cannot afford. Twenty-four 32-byte
// keys are ~1.5 KB of hex before coordinates and relays; terminals wrap that and
// chat clients truncate it. A comma-delimited list TRUNCATED in transit still
// parses, as a SHORTER well-formed list, so the reader opens a portfolio that
// looks complete while boards are silently missing. That is ready-62d1's lesson
// aimed at a new target: a damaged key-bearing link must fail VISIBLY.
//
// The format below declares every count BEFORE the entries it governs and
// requires the buffer to be consumed EXACTLY, so NO PROPER PREFIX OF A VALID BLOB
// IS A VALID BLOB. Truncation is always an error, and an error surfaces as the
// page's "ask whoever shared it for a fresh link" notice. The decoder is
// web/board/src/lib/portfoliokeys.ts (the browser is the only consumer), and
// portfoliokeys.test.ts proves the truncation property over EVERY prefix length
// of blobs emitted by this file.
//
// SIZE IS THE SECOND REASON, NOT THE FIRST. Base64url carries a 32-byte key in
// 43 characters where hex needs 64, and grouping by owner writes the 32-byte
// owner pubkey once per owner instead of once per board (32 bytes instead of 768
// for a single-owner portfolio). Measured end to end on the real 24-board
// portfolio that is 1552 characters against ~1849 for the board-scoped hex
// grammar — a real saving, but a modest one, and both would technically fit a
// browser. The anti-truncation property above is what actually decided the
// format. TestEncodePortfolioKeyBlob_TwentyFourBoardsFitInALink pins both
// numbers so this paragraph cannot quietly become false.

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"sort"
)

// PortfolioBlobVersion is the first byte of the blob. A decoder that does not
// recognize it must reject the WHOLE link rather than skip the blob: half a
// portfolio silently rendered as placeholders is the outcome this format exists
// to prevent.
//
// Layout, version 1 (integers big-endian, no padding, no alignment):
//
//	u8   version = 1
//	u8   ownerCount            (1..255)
//	repeated ownerCount times:
//	  [32]byte ownerPubkey     (raw, not hex)
//	  u8   boardCount          (1..255)
//	  repeated boardCount times:
//	    u8      boardDLen      (1..255, BYTES not runes)
//	    []byte  boardD         (UTF-8)
//	    u8      epochCount     (1..255)
//	    repeated epochCount times:
//	      u32     epoch        (>= 1)
//	      [32]byte cek
//
// then EOF — trailing bytes are an error, exactly as missing bytes are.
const PortfolioBlobVersion = 1

// EncodePortfolioKeyBlob renders per-board key material (board coordinate ->
// epoch -> CEK) as the base64url payload of a link's `keys=` parameter.
//
// Ordering is fully determined — owners ascending, boards ascending within an
// owner, epochs ascending within a board — so the same key material always
// produces the same bytes. A link is something users compare, re-mint and diff;
// map iteration order must not leak into it.
//
// Every count is range-checked, and an out-of-range one is an ERROR rather than a
// silent truncation: a 256th board must break the command, not quietly vanish
// from the link.
func EncodePortfolioKeyBlob(boards map[string]map[int][32]byte) (string, error) {
	if len(boards) == 0 {
		return "", fmt.Errorf("sync: portfolio key blob: no boards — a link carrying no key must omit keys= entirely")
	}

	byOwner := map[string][]string{}
	for coord := range boards {
		owner, boardD, ok := ParseBoardCoord(coord)
		if !ok {
			return "", fmt.Errorf("sync: portfolio key blob: board coordinate %q is malformed", coord)
		}
		byOwner[owner] = append(byOwner[owner], boardD)
	}

	owners := make([]string, 0, len(byOwner))
	for owner := range byOwner {
		owners = append(owners, owner)
	}
	sort.Strings(owners)

	buf := []byte{PortfolioBlobVersion}
	n, err := portfolioCount("owner", len(owners))
	if err != nil {
		return "", err
	}
	buf = append(buf, n)

	for _, owner := range owners {
		raw, derr := hex.DecodeString(owner)
		if derr != nil || len(raw) != 32 {
			return "", fmt.Errorf("sync: portfolio key blob: owner pubkey %q is not 32 bytes of hex", owner)
		}
		buf = append(buf, raw...)

		boardDs := byOwner[owner]
		sort.Strings(boardDs)
		nb, berr := portfolioCount("board", len(boardDs))
		if berr != nil {
			return "", berr
		}
		buf = append(buf, nb)

		for _, boardD := range boardDs {
			nd, derr := portfolioCount("board d-tag byte", len(boardD))
			if derr != nil {
				return "", fmt.Errorf("sync: portfolio key blob: board %q: %w", boardD, derr)
			}
			buf = append(buf, nd)
			buf = append(buf, boardD...)

			ceks := boards[BoardCoord(owner, boardD)]
			epochs := make([]int, 0, len(ceks))
			for ep := range ceks {
				epochs = append(epochs, ep)
			}
			sort.Ints(epochs)
			ne, eerr := portfolioCount("epoch", len(epochs))
			if eerr != nil {
				return "", fmt.Errorf("sync: portfolio key blob: board %q: %w", boardD, eerr)
			}
			buf = append(buf, ne)

			for _, ep := range epochs {
				if ep < 1 || int64(ep) > int64(^uint32(0)) {
					return "", fmt.Errorf("sync: portfolio key blob: board %q epoch %d is outside the representable range (1..%d)", boardD, ep, ^uint32(0))
				}
				var e4 [4]byte
				binary.BigEndian.PutUint32(e4[:], uint32(ep))
				buf = append(buf, e4[:]...)
				cek := ceks[ep]
				buf = append(buf, cek[:]...)
			}
		}
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// portfolioCount range-checks one of the format's u8 counts.
func portfolioCount(what string, n int) (byte, error) {
	if n < 1 || n > 255 {
		return 0, fmt.Errorf("sync: portfolio key blob: %d %ss does not fit the link format (1..255) — this link cannot represent that portfolio", n, what)
	}
	return byte(n), nil
}
