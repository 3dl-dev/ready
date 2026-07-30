// The LIVE measurement of the relay window the paging walk exists to defeat
// (ready-bec).
//
// WHY THIS FILE EXISTS SEPARATELY FROM negentropy_paging_test.go. The defect is
// a RELAY behaviour: one query reconciles at most a bounded number of records and
// nothing in NIP-77 says whether that bound was reached. cappedRelay (the
// in-process fake in negentropy_paging_test.go) MODELS that behaviour so the walk
// can be driven deterministically at a chosen bound — but a model is worth only
// what its agreement with the modelled thing is worth, and until this file the
// only evidence that a real relay behaves that way lived as prose in a commit
// message. A regression reintroducing the truncation would have been caught by
// the fake and by nothing that touched a real relay.
//
// So this test measures the three cappedRelay behaviours against a real strfry,
// with rd's own shipped board filter, and then proves the walk recovers a whole
// board across them:
//
//	one query answers at most `limit` records   (the truncation)
//	it picks the NEWEST ones                    (so `until` can walk backwards)
//	it honours `until`                          (so the walk terminates)
//	NegentropySync -> the whole board, pages>1  (the fix)
//
// It is a CONTROLLED experiment: a board of a size this file chose, on a relay it
// publishes to. The field counterpart lives at the bottom of this file —
// TestLiveRelay_FreshCloneOfTheProductionBoardIsWhole reads the real 3dl board off
// wss://relay.3dl.network, the relay/board pair the truncation was first measured
// on and the one ready-bec's done condition names. Neither subsumes the other: a
// change in 3dl's clamping is invisible to this test, and a board this test can
// characterise is not available there.
//
// Gated behind RD_NOSTR_LIVE_RELAY=1, like every other live proof in this
// package (ready-5fd made that gate honest: these proofs RUN, they do not skip).
package sync

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3dl-dev/ready/pkg/nostr"
)

// liveCapBoardSize is how many events this proof puts on the relay. It must
// EXCEED the window rd asks for (SyncPageLimit), or "the relay truncated" and
// "the board is small" are indistinguishable and nothing here measures anything.
// +60 clears it while keeping the publish cheap and the walk short: measured
// pages=3 — the 500-record window, the 60 below it, and the closing window that
// finds the cursor cannot move (the walk terminates on the CURSOR, never on a
// window's size; see NegentropySync).
const liveCapBoardSize = SyncPageLimit + 60

// TestLiveRelay_OneQueryIsBoundedAndTheWalkPagesPastIt is the item's live proof,
// and the check on the fake that stands in for the relay everywhere else.
//
// It publishes liveCapBoardSize distinct cards to an isolated, per-run board on a
// REAL relay — a board of known size, which is what makes every count below
// meaningful — and then measures:
//
//  1. THE BOUND IS REAL. One NIP-77 exchange carrying the filter rd's walk
//     actually ships (limit=SyncPageLimit, no cursor) reconciles exactly
//     SyncPageLimit of the liveCapBoardSize events, reports no error, and gives
//     the client no way to tell that from a board of exactly that size. That is
//     the silent truncation, measured rather than asserted.
//  2. IT IS THE NEWEST SLICE, AND `until` MOVES IT. The bound window holds the
//     newest records; re-asking with `until` at that window's floor returns the
//     older ones. These are the two properties the `until` walk stands on and the
//     two cappedRelay reproduces.
//  3. THE WALK DEFEATS IT. A fresh empty log syncing the same board downloads
//     every event over more than one window, projects every card, and converges:
//     the second sync moves zero event bytes.
//
// It also RECORDS, without asserting, what the same exchange returns with no
// limit at all — the relay's own default bound, which is configuration this repo
// does not own and which differs per relay (see SyncPageLimit's doc for the
// wss://relay.3dl.network measurement). The walk must not depend on it, and this
// test does not.
//
// No `authors` filter is used anywhere: it under-returns on relay.3dl.network
// (ready-5c5), which would confound a measurement whose entire point is counting.
func TestLiveRelay_OneQueryIsBoundedAndTheWalkPagesPastIt(t *testing.T) {
	if os.Getenv("RD_NOSTR_LIVE_RELAY") != "1" {
		t.Skip("set RD_NOSTR_LIVE_RELAY=1 to run the live relay-window measurement")
	}
	relay := liveRelayURL(t)
	k := liveRelayKey(t) // stable author: this proof reads its own writes back
	boardD := liveTestBoardD(t)
	boardCoord := BoardCoord(k.PubKeyHex(), boardD)
	t.Logf("live relay: %s  board: %s", relay, boardCoord)

	// A board of a KNOWN size, one event per second so the `until` cursor has
	// somewhere to step. Signed directly rather than through Publisher because
	// this proof needs exact control of created_at and of how many events exist.
	base := time.Now().Unix() - int64(liveCapBoardSize) - 10
	events := pagingBoard(t, k, boardCoord, liveCapBoardSize, base)
	newest := events[len(events)-1].CreatedAt

	ctx := context.Background()
	pctx, pcancel := context.WithTimeout(ctx, 5*time.Minute)
	acks, err := GuardedPublishMany(pctx, relay, events, false)
	pcancel()
	if err != nil {
		t.Fatalf("publish %d events: %v", liveCapBoardSize, err)
	}
	for i, a := range acks {
		if a.Err != nil {
			t.Fatalf("publish event %d (%s): %v", i, a.EventID, a.Err)
		}
		if !a.Accepted {
			t.Fatalf("relay refused event %d (%s): %q — this proof needs the whole board on the relay", i, a.EventID, a.Message)
		}
	}
	t.Logf("published %d distinct cards (created_at %d..%d)", len(events), base, newest)

	filter := BoardSyncFilter(boardCoord, nil)
	reconcile := func(f map[string]any) []string {
		t.Helper()
		rctx, rcancel := context.WithTimeout(ctx, 60*time.Second)
		r, err := nostr.NegentropyReconcile(rctx, relay, f, nil)
		rcancel()
		if err != nil {
			t.Fatalf("reconcile %v: %v", f, err)
		}
		return r.Need
	}

	// Context, not an assertion: what the relay volunteers when the client names
	// no window at all. This is per-relay configuration rd neither owns nor can
	// read back, and the walk is built so it does not matter.
	t.Logf("CONTEXT: one exchange with NO limit reconciled %d of %d events on %s",
		len(reconcile(filter)), liveCapBoardSize, relay)

	// (1) THE BOUND, measured with the filter the shipped walk builds for its
	// first window. An empty local set makes `need` exactly the record count the
	// relay was willing to reconcile.
	first := syncWindowFilter(filter, 0, false)
	need := reconcile(first)
	t.Logf("MEASURED: one exchange at limit=%d reconciled %d of %d events", SyncPageLimit, len(need), liveCapBoardSize)
	if len(need) != SyncPageLimit {
		t.Fatalf("a single exchange asking for limit=%d returned %d records of a %d-event board — cappedRelay models the relay as answering exactly the limit, and it does not. Re-derive the model against %s",
			SyncPageLimit, len(need), liveCapBoardSize, relay)
	}
	if len(need) >= liveCapBoardSize {
		t.Fatalf("one query answered with the whole %d-event board — nothing here measures a bounded window", liveCapBoardSize)
	}

	// (2) That slice is the NEWEST records, and `until` moves the window to the
	// older ones. Both are load-bearing: newest-first is why walking `until`
	// backwards terminates, and an ignored `until` would make the walk re-ask the
	// same window forever (which MaxSyncPages would then abort).
	served := map[string]bool{}
	for _, id := range need {
		served[id] = true
	}
	oldestServed := int64(1 << 62)
	for _, e := range events {
		if served[e.ID] && e.CreatedAt < oldestServed {
			oldestServed = e.CreatedAt
		}
	}
	if oldestServed != newest-int64(SyncPageLimit)+1 {
		t.Fatalf("the bounded window is not the NEWEST %d records: its floor is created_at=%d, want %d",
			SyncPageLimit, oldestServed, newest-int64(SyncPageLimit)+1)
	}
	older := reconcile(syncWindowFilter(filter, oldestServed-1, true))
	t.Logf("MEASURED: the same query with until=%d returned %d older records", oldestServed-1, len(older))
	if len(older) != liveCapBoardSize-SyncPageLimit {
		t.Fatalf("until=%d must return the %d records below the first window, got %d — `until` is not bounding the relay's window",
			oldestServed-1, liveCapBoardSize-SyncPageLimit, len(older))
	}
	for _, id := range older {
		if served[id] {
			t.Fatalf("the until-bounded window re-served event %s from the first window — the cursor does not exclude", id)
		}
	}

	// (3) THE WALK. A fresh clone: empty log, same board, paged sync.
	log := NewNostrLog(filepath.Join(t.TempDir(), NostrLogFile))
	trusted := map[string]bool{k.PubKeyHex(): true}
	res, err := NegentropySync(ctx, relay, log, filter, trusted, 120*time.Second, false)
	if err != nil {
		t.Fatalf("paged sync: %v", err)
	}
	t.Logf("PAGED: need=%d downloaded=%d pages=%d (one window stops at %d)",
		res.Need, res.Downloaded, res.Pages, SyncPageLimit)
	if res.Pages < 2 {
		t.Fatalf("a %d-event board at a %d-record window must take more than one window, walk used pages=%d",
			liveCapBoardSize, SyncPageLimit, res.Pages)
	}
	if res.Downloaded != liveCapBoardSize {
		t.Fatalf("a fresh clone must download the WHOLE board: got downloaded=%d of %d (need=%d pages=%d)",
			res.Downloaded, liveCapBoardSize, res.Need, res.Pages)
	}

	// The substance of the done condition: the fresh clone PROJECTS every card,
	// not merely holds every event.
	items := ProjectItems(mustReadAll(t, log), ProjectOptions{Trusted: trusted})
	var missing []string
	for i := 0; i < liveCapBoardSize; i++ {
		id := fmt.Sprintf("ready-pg%04d", i)
		if items[id] == nil {
			missing = append(missing, id)
		}
	}
	if len(missing) != 0 {
		t.Fatalf("fresh clone projected %d of %d cards — %d missing, first %v",
			len(items), liveCapBoardSize, len(missing), missing[:min(5, len(missing))])
	}

	// And it CONVERGES: syncing again moves nothing. Pre-fix a re-sync moved
	// nothing too — while short — which is exactly why silence was the defect.
	res2, err := NegentropySync(ctx, relay, log, filter, trusted, 120*time.Second, false)
	if err != nil {
		t.Fatalf("re-sync: %v", err)
	}
	if res2.Downloaded != 0 || res2.Uploaded != 0 || res2.EventBytesDownloaded != 0 || res2.EventBytesUploaded != 0 {
		t.Fatalf("converged re-sync must move zero event bytes, got downloaded=%d uploaded=%d down=%dB up=%dB",
			res2.Downloaded, res2.Uploaded, res2.EventBytesDownloaded, res2.EventBytesUploaded)
	}
	t.Logf("PROVEN against %s: one query is bounded at %d newest records and says nothing; `until` walks past it; the fresh clone fetched all %d over %d windows and the re-sync moved 0 event bytes",
		relay, SyncPageLimit, liveCapBoardSize, res.Pages)
}

// ---------------------------------------------------------------------------
// The configured relay: a cap STRICTLY BELOW the limit rd asks for
// ---------------------------------------------------------------------------

// The relay this experiment needs — one whose per-REQ cap is BELOW the limit rd
// asks for — does not exist in the wild: no public relay is configured that way,
// and neither relay this repo can otherwise reach could be told to be. For two
// revisions that made the sub-limit cap the one load-bearing premise under
// cappedRelay measured against nothing, and for one more it made the measurement
// a transcript in a commit message that nobody else could re-run, because the
// test skipped unless an operator had already stood a relay up by hand.
//
// So the test stands its own up. strfry's cap is its own `maxFilterLimit`, the
// image is small, and the container is ready in about a second — measured 0.5s
// wall for the whole proof once the image is local. That turns the central
// premise from a one-off transcript into something any checkout can re-derive.
const (
	// subCapRelayImageDefault is the strfry distribution the experiment runs
	// against. Overridable via RD_NOSTR_SUBCAP_RELAY_IMAGE so a CI runner can
	// pin a digest; the behaviours it is here to measure are ASSERTED below, so
	// a distribution that behaves differently fails loudly rather than quietly
	// measuring something else.
	subCapRelayImageDefault = "dockurr/strfry:latest"

	// subCapRelayMaxFilterLimit is the cap written into that relay's config. It
	// must be strictly below BOTH SyncPageLimit (or the window rd asks for is
	// never clamped) and subCapBoardSize (or nothing is withheld) — the
	// discriminating band the test asserts it is standing in.
	subCapRelayMaxFilterLimit = 400

	// subCapRelayContainerPrefix names the containers this file creates, so a
	// crashed run leaves something greppable: `docker ps -a | grep rd-strfry-subcap`.
	subCapRelayContainerPrefix = "rd-strfry-subcap"
)

// subCapRelayURL returns a relay whose per-REQ cap is below SyncPageLimit,
// standing one up if the environment has not supplied one.
//
// RD_NOSTR_SUBCAP_RELAY_URL still wins when set: an operator who has a suitably
// configured relay already running (or is on a host without docker) can point the
// proof at it, and no container is created. The relay it names must have a
// per-REQ cap strictly below subCapBoardSize, or the test says so and fails.
//
// STANDING ONE UP BY HAND. Every line below was run verbatim on 2026-07-30 and
// the test passed against the result; the earlier revision of this doc omitted
// the tmpfs mount and strfry died on the spot with
// `strfry error: mdb_env_open: No such file or directory` (see startSubCapRelay
// for why). From an empty scratch directory:
//
//	docker run --rm --entrypoint cat dockurr/strfry:latest \
//	  /etc/strfry.conf.default > strfry.conf
//	sed -i 's/maxFilterLimit = 500/maxFilterLimit = 400/' strfry.conf
//	sed -i 's|plugin = "/app/write-policy.py"|plugin = ""|' strfry.conf
//	docker run -d --name rd-strfry-cap400 -p 127.0.0.1:17777:7777 \
//	  --tmpfs /app/strfry-db \
//	  -v "$PWD/strfry.conf:/etc/strfry.conf" dockurr/strfry:latest
//	RD_NOSTR_LIVE_RELAY=1 RD_NOSTR_SUBCAP_RELAY_URL=ws://127.0.0.1:17777 \
//	  go test ./pkg/sync/ -run TestLiveRelay_ASubLimitCapped
//	docker rm -f rd-strfry-cap400
func subCapRelayURL(t *testing.T) string {
	t.Helper()
	if r := os.Getenv("RD_NOSTR_SUBCAP_RELAY_URL"); r != "" {
		t.Logf("using the relay named by RD_NOSTR_SUBCAP_RELAY_URL (%s); not starting a container", r)
		return r
	}
	return startSubCapRelay(t)
}

// startSubCapRelay runs a throwaway strfry with maxFilterLimit below
// SyncPageLimit and returns its ws:// URL. The container is removed when the test
// ends; its logs are dumped first if the test failed.
//
// THE THREE THINGS THAT MAKE THIS WORK, each of which cost a failed attempt:
//
//   - The db directory. `db = "./strfry-db/"` resolves to /app/strfry-db, and the
//     image does NOT contain it — strfry will not create it either, so a plain
//     `docker run` of this image dies immediately with
//     `strfry error: mdb_env_open: No such file or directory`. A tmpfs mount at
//     that path both creates it and keeps the throwaway board in RAM.
//   - The write policy. The image ships `plugin = "/app/write-policy.py"`, a
//     pubkey whitelist that NAKs every publish with
//     "blocked: pubkey … not in whitelist" — which from the client looks exactly
//     like an empty board. It has to be turned off.
//   - The cap itself, edited into the shipped default config rather than into a
//     config this file embeds, so the rest of the relay's settings stay upstream's.
//     Both edits are checked for having actually applied: if the upstream config
//     changes shape, this fails here with the reason rather than downstream with a
//     measurement that silently means nothing.
//
// The host port is assigned by docker (`-p 127.0.0.1::7777`) and read back, so
// concurrent runs on one machine do not collide over a fixed port.
func startSubCapRelay(t *testing.T) string {
	t.Helper()
	image := subCapRelayImageDefault
	if env := os.Getenv("RD_NOSTR_SUBCAP_RELAY_IMAGE"); env != "" {
		image = env
	}

	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Fatalf("this proof stands its own sub-limit-capped relay up and no `docker` is on PATH (%v). Either install docker, or run a strfry with maxFilterLimit < %d yourself and name it in RD_NOSTR_SUBCAP_RELAY_URL (see startSubCapRelay for what the config needs). This is NOT skipped: it is the only live witness that a relay clamps rd's DOWNLOAD below what negentropy promised, which is the premise the paging walk exists to defeat.",
			err, SyncPageLimit)
	}
	run := func(timeout time.Duration, args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		out, err := exec.CommandContext(ctx, docker, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}

	// The daemon has to be reachable before anything below means anything: a
	// docker binary with no daemon behind it fails four different ways otherwise.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	if out, err := exec.CommandContext(ctx, docker, "version", "--format", "{{.Server.Version}}").CombinedOutput(); err != nil {
		cancel()
		t.Fatalf("docker is installed but its daemon is not reachable (%v):\n%s\nStart it, or name an already-running sub-limit-capped relay in RD_NOSTR_SUBCAP_RELAY_URL.", err, out)
	}
	cancel()

	pctx, pcancel := context.WithTimeout(context.Background(), 5*time.Minute)
	if err := exec.CommandContext(pctx, docker, "image", "inspect", image).Run(); err != nil {
		pcancel()
		t.Logf("pulling %s (not present locally)", image)
		run(5*time.Minute, "pull", image)
	} else {
		pcancel()
	}

	// The shipped default config, with exactly two edits.
	conf := run(60*time.Second, "run", "--rm", "--entrypoint", "cat", image, "/etc/strfry.conf.default")
	patch := func(s, old, new string) string {
		t.Helper()
		if n := strings.Count(s, old); n != 1 {
			t.Fatalf("%s's /etc/strfry.conf.default contains %d occurrences of %q, want exactly 1 — the image's config changed shape and this experiment can no longer configure it. Fix the substitution here; do not let it measure an unconfigured relay.", image, n, old)
		}
		return strings.Replace(s, old, new, 1)
	}
	conf = patch(conf, "maxFilterLimit = 500", fmt.Sprintf("maxFilterLimit = %d", subCapRelayMaxFilterLimit))
	conf = patch(conf, `plugin = "/app/write-policy.py"`, `plugin = ""`)
	confPath := filepath.Join(t.TempDir(), "strfry.conf")
	if err := os.WriteFile(confPath, []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}

	name := fmt.Sprintf("%s-%d", subCapRelayContainerPrefix, time.Now().UnixNano())
	run(2*time.Minute, "run", "-d", "--name", name,
		"-p", "127.0.0.1::7777",
		"--tmpfs", "/app/strfry-db", // the image has no db dir; strfry will not make one
		"-v", confPath+":/etc/strfry.conf:ro",
		image)
	t.Cleanup(func() {
		if t.Failed() {
			out, _ := exec.Command(docker, "logs", "--tail", "50", name).CombinedOutput()
			t.Logf("sub-cap relay %s logs:\n%s", name, out)
		}
		if out, err := exec.Command(docker, "rm", "-f", name).CombinedOutput(); err != nil {
			t.Logf("removing sub-cap relay container %s: %v\n%s", name, err, out)
		}
	})

	hostPort := run(30*time.Second, "port", name, "7777/tcp")
	if i := strings.IndexByte(hostPort, '\n'); i >= 0 {
		hostPort = strings.TrimSpace(hostPort[:i])
	}
	if hostPort == "" {
		t.Fatalf("docker port %s 7777/tcp returned nothing — the container is not publishing a host port", name)
	}
	url := "ws://" + hostPort

	// Ready when it answers a REQ, not merely when docker says "Up": strfry opens
	// its listener a beat after the process starts, and a publish into that beat
	// fails as a connection error that reads like a broken relay.
	deadline := time.Now().Add(60 * time.Second)
	for attempt := 1; ; attempt++ {
		rctx, rcancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, err := nostr.FetchMany(rctx, url, map[string]any{"kinds": []int{KindCard}, "limit": 1})
		rcancel()
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			logs, _ := exec.Command(docker, "logs", "--tail", "50", name).CombinedOutput()
			t.Fatalf("sub-cap relay %s (container %s) never answered a REQ after %d attempts: %v\n%s", url, name, attempt, err, logs)
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Logf("stood up a sub-limit-capped relay: %s (image %s, maxFilterLimit=%d, container %s)", url, image, subCapRelayMaxFilterLimit, name)
	return url
}

// subCapBoardSize sits strictly between the relay's cap and SyncPageLimit, which
// is the band where the two candidate termination rules DISAGREE: at 450 the
// NEG-OPEN window is short of the limit rd asked for (so "the window was small,
// we must be done" fires) while the download is short of the window (so events
// remain). Below the cap nothing is withheld; above SyncPageLimit both rules page.
const subCapBoardSize = 450

// TestLiveRelay_ASubLimitCappedRelayClampsTheDownloadSilently is the experiment
// that settles where a relay's cap actually bites, against a REAL strfry
// configured to have one, and it is the live witness for every behaviour
// cappedRelay models.
//
// MEASURED 2026-07-30 by a standalone probe against dockurr/strfry with
// maxFilterLimit=400 over a 600-event board. The rows are recorded here because
// the sweep across limits is wider than the proof needs; the three that carry the
// argument (NEG-OPEN honours the limit, the REQ clamps, the walk defeats it) are
// re-derived by the assertions below on every run, against a relay this test
// stands up itself:
//
//	NEG-OPEN limit=100/399/400/401/500 -> 100/399/400/401/500   HONOURED
//	NEG-OPEN limit=1000/5000/none      -> 600 (the whole board) HONOURED
//	REQ      limit=399                 -> 399
//	REQ      limit=400/401/500/5000    -> 400                   CLAMPED
//	REQ      no limit                  -> 400                   CLAMPED
//
// So `maxFilterLimit` does not apply to NIP-77 at all — negentropy answers exactly
// what the client asks for — and it clamps every NIP-01 REQ SILENTLY: no NOTICE,
// no CLOSED, just EOSE with fewer events than were requested. Neither of the two
// behaviours this file previously assumed (a loud NEG-ERR refusal; a silently
// clamped NEG-OPEN window) occurs.
//
// The consequence is the whole point: rd's download is a REQ, so negentropy can
// NAME 450 ids while the fetch DELIVERS 400, silently. Measured end to end on this
// relay with the two termination rules swapped under it:
//
//	relayWindow >= SyncPageLimit -> downloaded=400 of 450, pages=1, no error
//	the `until` cursor           -> downloaded=450,        pages=3
//
// A relay with this configuration has to be created on purpose, so this test
// creates one (startSubCapRelay) rather than waiting for an env var to name one.
// It therefore RUNS under the package's own live invocation
// `RD_NOSTR_LIVE_RELAY=1 go test ./pkg/sync/` — it does not skip, which is the
// point: the premise above is the one this whole file exists to hold, and a
// premise nobody can re-run is a transcript, not a proof.
func TestLiveRelay_ASubLimitCappedRelayClampsTheDownloadSilently(t *testing.T) {
	if os.Getenv("RD_NOSTR_LIVE_RELAY") != "1" {
		t.Skip("set RD_NOSTR_LIVE_RELAY=1 to run the live sub-limit-cap measurement")
	}
	relay := subCapRelayURL(t)
	k, err := nostr.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	// A fresh key and a per-run board d-tag: this relay is a throwaway, and the
	// counts below are only meaningful over a board of a size this test set.
	boardCoord := BoardCoord(k.PubKeyHex(), fmt.Sprintf("subcap-%d", time.Now().UnixNano()))
	t.Logf("sub-cap relay: %s  board: %s", relay, boardCoord)

	base := time.Now().Unix() - int64(subCapBoardSize) - 10
	events := pagingBoard(t, k, boardCoord, subCapBoardSize, base)
	ctx := context.Background()
	pctx, pcancel := context.WithTimeout(ctx, 5*time.Minute)
	acks, err := GuardedPublishMany(pctx, relay, events, false)
	pcancel()
	if err != nil {
		t.Fatalf("publish %d events: %v", subCapBoardSize, err)
	}
	for i, a := range acks {
		if a.Err != nil {
			t.Fatalf("publish event %d: %v", i, a.Err)
		}
		if !a.Accepted {
			t.Fatalf("relay refused event %d: %q — if this says \"not in whitelist\", the image's write-policy plugin is still enabled; see startSubCapRelay", i, a.Message)
		}
	}
	filter := BoardSyncFilter(boardCoord, nil)

	// (1) NEG-OPEN HONOURS THE LIMIT. The window rd's first page asks for comes
	// back naming every event on the board, because the board is smaller than the
	// limit — the relay's own 400-record cap does not touch this path at all.
	rctx, rcancel := context.WithTimeout(ctx, 60*time.Second)
	one, err := nostr.NegentropyReconcile(rctx, relay, syncWindowFilter(filter, 0, false), nil)
	rcancel()
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	t.Logf("MEASURED: NEG-OPEN at limit=%d named %d ids of a %d-event board", SyncPageLimit, len(one.Need), subCapBoardSize)
	if len(one.Need) != subCapBoardSize {
		t.Fatalf("NEG-OPEN at limit=%d named %d ids of %d — cappedRelay models this path as HONOURING the limit (maxFilterLimit not applied to NIP-77). Re-derive the model against %s",
			SyncPageLimit, len(one.Need), subCapBoardSize, relay)
	}
	if len(one.Need) >= SyncPageLimit {
		t.Fatalf("the board (%d) must be smaller than SyncPageLimit (%d) or the window-size rule pages anyway and nothing here discriminates",
			len(one.Need), SyncPageLimit)
	}

	// (2) THE REQ IS CLAMPED, SILENTLY, AND BELOW WHAT NEGENTROPY JUST PROMISED.
	// This is the premise cappedRelay.reqCap models and the one that had never
	// been measured. No error is returned: the truncation is invisible.
	fctx, fcancel := context.WithTimeout(ctx, 120*time.Second)
	fetched, err := nostr.FetchByIDs(fctx, relay, one.Need)
	fcancel()
	if err != nil {
		t.Fatalf("fetch %d ids: %v", len(one.Need), err)
	}
	t.Logf("MEASURED: a REQ for those %d ids returned %d events and NO error", len(one.Need), len(fetched))
	if len(fetched) >= len(one.Need) {
		t.Fatalf("%s served all %d requested ids — its per-REQ cap is not below the %d ids negentropy named, so this test measures nothing. The relay this test stands up is configured maxFilterLimit=%d; if RD_NOSTR_SUBCAP_RELAY_URL is set, that relay's cap must be below %d too",
			relay, len(fetched), len(one.Need), subCapRelayMaxFilterLimit, subCapBoardSize)
	}

	// (3) AND THE WALK STILL LANDS THE WHOLE BOARD. Under the retired
	// window-size rule this stops at len(fetched) with pages=1 and no error.
	log := NewNostrLog(filepath.Join(t.TempDir(), NostrLogFile))
	trusted := map[string]bool{k.PubKeyHex(): true}
	res, err := NegentropySync(ctx, relay, log, filter, trusted, 120*time.Second, false)
	if err != nil {
		t.Fatalf("paged sync: %v", err)
	}
	t.Logf("PAGED: need=%d downloaded=%d pages=%d", res.Need, res.Downloaded, res.Pages)
	if res.Downloaded != subCapBoardSize {
		t.Fatalf("a relay clamping the DOWNLOAD at %d silently truncated the sync: downloaded=%d of %d (pages=%d)",
			len(fetched), res.Downloaded, subCapBoardSize, res.Pages)
	}
	if res.Pages < 2 {
		t.Fatalf("the download was clamped at %d of %d, so the walk needed more than one window, got pages=%d",
			len(fetched), subCapBoardSize, res.Pages)
	}
	items := ProjectItems(mustReadAll(t, log), ProjectOptions{Trusted: trusted})
	if len(items) != subCapBoardSize {
		t.Fatalf("fresh clone projected %d of %d cards", len(items), subCapBoardSize)
	}
	res2, err := NegentropySync(ctx, relay, log, filter, trusted, 120*time.Second, false)
	if err != nil {
		t.Fatalf("re-sync: %v", err)
	}
	if res2.Downloaded != 0 || res2.Uploaded != 0 {
		t.Fatalf("converged re-sync moved events: downloaded=%d uploaded=%d", res2.Downloaded, res2.Uploaded)
	}
	t.Logf("PROVEN against %s: NEG-OPEN honours limit=%d (named %d), the REQ clamps to %d SILENTLY, and the cursor walk still merged all %d over %d windows",
		relay, SyncPageLimit, len(one.Need), len(fetched), res.Downloaded, res.Pages)
}

// ---------------------------------------------------------------------------
// The named measurement: the REAL board on the REAL relay the defect was found on
// ---------------------------------------------------------------------------

// prodRelayURL / prodBoardCoord name the exact relay and board ready-bec's done
// condition speaks about: "a fresh clone of the 3dl board projects all 536
// cards". Every other proof in this package runs against a board it published
// itself, which is what makes them controlled — and is also why none of them
// would have caught a regression against THIS relay and THIS board, the pair the
// truncation was actually measured on (an unpaged sync merged 481 events and
// projected 201 items of a 536-card board, twice, silently).
//
// The board coordinate is this repo's own pinned board (.ready/config.json
// "board"), whose D-tag is the reserved production one. This test only ever READS
// it: the local log starts empty so the walk has nothing to upload, and
// GuardedPublish is called with production=false, which refuses any event
// addressing that coordinate before a relay is dialled (guardReservedBoard). The
// Uploaded assertion below pins that read-only property rather than assuming it.
//
// Both are overridable so the proof can be pointed at another deployment.
func prodRelayURL() string {
	if r := os.Getenv("RD_NOSTR_PROD_RELAY_URL"); r != "" {
		return r
	}
	return "wss://relay.3dl.network"
}

func prodBoardCoord() string {
	if c := os.Getenv("RD_NOSTR_PROD_BOARD_COORD"); c != "" {
		return c
	}
	return "30301:a9f766ae56bbf466d2d361e5b1788b7cd689fd8e3b418e35b002b313f478db25:ready"
}

// prodBoardCardFloor is the card count measured on that board on 2026-07-30 and
// written into ready-bec's done condition. It is asserted as a FLOOR, not an
// equality: rd's boards only gain cards, so a hard 536 would go red the next time
// anyone files an item, and a flaky proof proves nothing. The floor still fails
// loudly on the defect it exists for — the truncated sync projected 201.
const prodBoardCardFloor = 536

// pagedCoordWalk is the INDEPENDENT ground truth: a plain NIP-01 REQ walk over an
// `#a` coordinate filter, paged on `until` at limit >= 500, run with none of the
// code under test. It deliberately shares nothing with NegentropySync — different
// protocol (REQ/EOSE, not NIP-77), its own filter construction, its own cursor —
// so "the sync got everything" is checked against a second opinion rather than
// against itself.
//
// No `authors` filter appears here or anywhere in this test: it silently
// under-returns on relay.3dl.network (ready-5c5 measured 42 of 56 boards), which
// would confound a measurement whose entire point is counting.
func pagedCoordWalk(ctx context.Context, t *testing.T, relay string, base map[string]any) map[string]*nostr.Event {
	t.Helper()
	seen := map[string]*nostr.Event{}
	var until int64
	bounded := false
	for page := 1; page <= MaxSyncPages; page++ {
		f := map[string]any{"limit": SyncPageLimit}
		for k, v := range base {
			f[k] = v
		}
		if bounded {
			f["until"] = until
		}
		fctx, cancel := context.WithTimeout(ctx, 90*time.Second)
		evs, err := nostr.FetchMany(fctx, relay, f)
		cancel()
		if err != nil {
			t.Fatalf("ground-truth walk page %d (until=%d): %v", page, until, err)
		}
		oldest := int64(1<<62 - 1)
		fresh := 0
		for _, e := range evs {
			if e == nil {
				continue
			}
			if e.CreatedAt < oldest {
				oldest = e.CreatedAt
			}
			if _, dup := seen[e.ID]; !dup {
				seen[e.ID] = e
				fresh++
			}
		}
		t.Logf("ground truth page %d: until=%d served=%d new=%d cumulative=%d", page, until, len(evs), fresh, len(seen))
		if len(evs) == 0 {
			break
		}
		if bounded && oldest >= until {
			break
		}
		until = oldest
		bounded = true
	}
	return seen
}

// cardIDs returns the distinct rd item ids (30302 "d" tags) among a set of events
// — the unit ready-bec's done condition counts.
func cardIDs(events map[string]*nostr.Event) map[string]bool {
	out := map[string]bool{}
	for _, e := range events {
		if e.Kind == KindCard {
			if d := tagValue(e, "d"); d != "" {
				out[d] = true
			}
		}
	}
	return out
}

// TestLiveRelay_FreshCloneOfTheProductionBoardIsWhole takes the measurement
// ready-bec's done condition NAMES and nothing on this branch had taken: a fresh
// clone of the 3dl board, from wss://relay.3dl.network, holding every event and
// projecting every card.
//
// The sibling proof (TestLiveRelay_OneQueryIsBoundedAndTheWalkPagesPastIt) is the
// controlled experiment — a board of a size this repo chose, on a relay whose cap
// this repo can characterise. This one is the FIELD measurement, and the two catch
// different regressions: the controlled proof would stay green if
// relay.3dl.network changed its cap, its clamping behaviour, or its handling of a
// 1800-event coordinate, because it never touches it. The done condition names
// this relay and this board precisely because that is where the truncation was
// observed.
//
// Read-only by construction: empty local log (nothing to upload) plus
// production=false (GuardedPublish refuses the reserved production coordinate
// before dialling). Uploaded==0 is asserted, not assumed.
func TestLiveRelay_FreshCloneOfTheProductionBoardIsWhole(t *testing.T) {
	if os.Getenv("RD_NOSTR_LIVE_RELAY") != "1" {
		t.Skip("set RD_NOSTR_LIVE_RELAY=1 to run the live production-board fresh-clone proof")
	}
	relay := prodRelayURL()
	coord := prodBoardCoord()
	t.Logf("live production relay: %s  board: %s", relay, coord)

	ctx := context.Background()
	filter := BoardSyncFilter(coord, nil)

	// (0) GROUND TRUTH FIRST, and deliberately first: the board is live, so
	// anything published between this walk and the sync can only make the sync
	// hold MORE. Every assertion below is therefore monotone-safe.
	truth := pagedCoordWalk(ctx, t, relay, filter)
	truthCards := cardIDs(truth)
	t.Logf("GROUND TRUTH (independent paged #a REQ walk): %d events, %d distinct cards", len(truth), len(truthCards))
	if len(truth) <= SyncPageLimit {
		t.Fatalf("the board on %s holds %d events, which is not more than one window (%d) — this proof cannot measure paging against it. Point RD_NOSTR_PROD_BOARD_COORD at a board bigger than %d.",
			relay, len(truth), SyncPageLimit, SyncPageLimit)
	}
	if len(truthCards) < prodBoardCardFloor {
		t.Fatalf("the independent walk found %d cards on %s, below the %d measured into ready-bec's done condition — either the walk regressed or the board lost cards",
			len(truthCards), coord, prodBoardCardFloor)
	}

	// (1) THE TRUNCATION, on this relay, with the filter rd's first window ships.
	// An empty local set makes `need` exactly what one query was willing to give.
	rctx, rcancel := context.WithTimeout(ctx, 90*time.Second)
	one, err := nostr.NegentropyReconcile(rctx, relay, syncWindowFilter(filter, 0, false), nil)
	rcancel()
	if err != nil {
		t.Fatalf("single-window reconcile: %v", err)
	}
	t.Logf("MEASURED: ONE exchange at limit=%d reconciled %d of the %d events on this board", SyncPageLimit, len(one.Need), len(truth))
	if len(one.Need) >= len(truth) {
		t.Fatalf("one query answered with all %d events — %s no longer truncates, so this proof measures nothing", len(truth), relay)
	}

	// (2) THE FRESH CLONE. Trust gate disabled (nil) on purpose: this measures the
	// PAGING walk, and a trust set would make the count a function of which keys
	// hold grants on the live board. The gate is proven separately, against a
	// controlled board, by TestLiveRelay_NegentropyDownloadTrustGate.
	log := NewNostrLog(filepath.Join(t.TempDir(), NostrLogFile))
	res, err := NegentropySync(ctx, relay, log, filter, nil, 180*time.Second, false)
	if err != nil {
		t.Fatalf("fresh-clone sync: %v", err)
	}
	t.Logf("FRESH CLONE: need=%d downloaded=%d uploaded=%d pages=%d", res.Need, res.Downloaded, res.Uploaded, res.Pages)
	if res.Uploaded != 0 {
		t.Fatalf("this proof must be READ-ONLY against the production board, it uploaded %d events", res.Uploaded)
	}
	if res.Pages < 2 {
		t.Fatalf("a %d-event board behind a %d-record window must take more than one window, walk used pages=%d",
			len(truth), len(one.Need), res.Pages)
	}

	// (3) THE CLONE HOLDS EVERYTHING THE INDEPENDENT WALK FOUND. This is the
	// assertion the truncation fails: the measured defect merged 481 of them.
	stored, err := log.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, e := range stored {
		if e != nil {
			have[e.ID] = true
		}
	}
	var missing []string
	for id := range truth {
		if !have[id] {
			missing = append(missing, id)
		}
	}
	if len(missing) != 0 {
		t.Fatalf("fresh clone is SHORT: it holds %d of the %d events the independent walk found on %s — %d missing, first %v",
			len(have), len(truth), relay, len(missing), missing[:min(5, len(missing))])
	}

	// (4) AND IT PROJECTS EVERY CARD — the done condition's own words.
	items := ProjectItems(stored, ProjectOptions{})
	var unprojected []string
	for id := range truthCards {
		if items[id] == nil {
			unprojected = append(unprojected, id)
		}
	}
	if len(unprojected) != 0 {
		t.Fatalf("fresh clone projected %d items but %d of the %d cards on the board are missing, first %v",
			len(items), len(unprojected), len(truthCards), unprojected[:min(5, len(unprojected))])
	}
	if len(items) < prodBoardCardFloor {
		t.Fatalf("fresh clone projected %d items, below the %d ready-bec's done condition names (the truncated sync projected 201)",
			len(items), prodBoardCardFloor)
	}
	t.Logf("PROVEN against %s: one query gives %d of %d events; the paged walk gives all %d over %d windows and the fresh clone projects %d items (>= the %d named in ready-bec)",
		relay, len(one.Need), len(truth), len(truth), res.Pages, len(items), prodBoardCardFloor)
}
