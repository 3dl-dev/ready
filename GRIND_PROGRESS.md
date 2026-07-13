# ready-cb6 — I7 capstone: remove campfire SDK, full green nostr-native

Goal: zero `campfire-net/campfire` (cf-protocol / cf-conventions / cf-authority /
pkg/identity / pkg/naming / pkg/beacon) imports in *.go. `.campfire/`/`.cf/` never
touched. `go build ./...` and `go test ./... -timeout 600s` fully green, zero skips.

Keep `/home/baron/go1.25/bin/go build ./...` GREEN before every commit. Tree MAY be
red between sessions (allowed) but each commit builds.

---

## STRATEGY (the seam)

The dominant coupling is the record data type `store.MessageRecord` (was ~167
non-test refs). Introduced native replacement **`pkg/msgrec.MessageRecord`** — a
self-contained, campfire-free record the derivation engine replays. Cutover order:

1. (DONE S1) Flip the pure data-type consumers (derive engine + mutation log) to
   `msgrec`, converting `store.MessageRecord -> msgrec.MessageRecord` at each
   remaining store boundary with a tiny adapter.
2. (TODO) Delete the store-backed (sqlite) read path so no adapter is needed:
   `state.DeriveFromStore`/`DeriveAllFromStore`, and repoint every caller to the
   JSONL/nostr path. This unthreads `store.Store` from the read spine
   (`root.go: allItemsFromJSONLOrStore(s)`, `crossdep.ApplyBlocking(items, s, ...)`,
   `resolve.*`, `list/label/dep/create/engage`).
3. (TODO) Delete the campfire WRITE + init + join + conventionserver paths.
4. (TODO) Delete campfire-vestigial e2e tests (delete-with-code, not skip).
5. (TODO) Drop the SDK from go.mod/go.sum; tighten no-.cf/no-store.db invariants.

Baseline at start of S1: 74 files import the SDK; `go build ./...` green;
`main..HEAD` empty (branch sat at main — I1-I6 already merged to base).

---

## DONE (session 1)

- **NEW `pkg/msgrec/msgrec.go`** — native `MessageRecord{ID,CampfireID,Sender,
  Payload,Tags,Antecedents,Timestamp,Signature,ReceivedAt}`. No SDK import (only a
  historical comment reference). This is the type the engine now replays.
- **`pkg/state/state.go`** — derivation engine fully on `msgrec.MessageRecord`
  (Derive/DeriveAll/all handlers/JSONL conversions). `DeriveFromStore`/
  `DeriveAllFromStore` retained as thin **store->msgrec shims** via new
  `msgrecsFromStore(...)` (state.go still imports `store` ONLY for Store/
  MessageFilter in those two shims — removed when step 2 deletes them).
- **`pkg/state/*_test.go`** (11 files: derive_jsonl, integration, state_bugfix,
  state_crossdep, state_dep, state_fulfillment, state_gate, state_malformed,
  state_new_ops, state_serverbinding_regression, state_test) — repointed
  `store.MessageRecord -> msgrec.MessageRecord` (import swapped). NOT weakened —
  same records, native type. `views_jsonl_test.go` only names it in a comment.
- **`pkg/jsonl/types.go` + `reader_test.go`** — fully de-SDK'd. `MutationRecord`
  From/ToMessageRecord now use `msgrec.MessageRecord`. jsonl imports NO SDK now.
- **`pkg/sync/inbound.go`** — the store->JSONL pull boundary now converts via new
  local `msgrecFromStore(...)` before `jsonl.FromMessageRecord`. inbound.go still
  imports `store` for the campfire Store/MessageFilter/ListMessages (deleted in
  step 3 when the campfire pull path goes).

Verify S1: `go build ./...` GREEN. `go test ./pkg/state ./pkg/jsonl ./pkg/sync` GREEN.
SDK-importing files 74 -> 62. cf-protocol/store import sites 51 -> 39.

---

## DONE (session 2) — SDK files 62 -> 57

**S2a — pkg/state fully SDK-free (commit d6843b4).** Relocated the store->derive
shim OUT of pkg/state into a NEW isolated package **`pkg/storederive`**
(`FromStore`/`AllFromStore` + `msgrecsFromStore`). This is now the SOLE remaining
store-backed derive surface — step 3 deletes it wholesale once the write path and
storetest harness stop needing it. Repointed every caller (resolve, crossdep,
list/create/dep/engage/label, storetest harness Derive/DeriveAll, and the
state_test/crossdep_test/integration test callers). `pkg/state/state.go` no longer
imports the SDK. Zero behaviour change, zero test deletion.

**S2b — store-backed READ path deleted (commit a43c6fb).** The nostr board is a
single project, so there is no multi-campfire store topology to read:
- **`pkg/resolve`** — deleted store-backed `ByID`/`ByIDExact`/`AllItems`/
  `AllItemsInCampfire`; resolve is now **SDK-free** (JSONL/nostr resolvers only:
  `ByIDFromJSONL`/`ByIDFromJSONLExact`/`AllItemsFromJSONL`).
- **`pkg/crossdep`** — DELETED entirely (`crossdep.go` + tests). Cross-campfire dep
  blocking was a campfire-federation feature requiring a multi-campfire store.
- **`cmd/rd/root.go`** — `allItemsFromJSONLOrStore`/`byIDFromJSONLOrStore`/
  `byIDFromJSONLOrStoreExact` now resolve store-free (nostr projection, else JSONL,
  else empty/ErrNotFound). Store param retained as `_ store.Store` (unused) so the
  ~40 call sites need not change ahead of the write-path cut. Dropped `crossdep` +
  `naming` imports.
- **`cmd/rd/show.go`** — removed the cross-campfire dep display + `crossdep`/`naming`
  imports; JSON path simplified to `enc.Encode(item)`.
- **Tests (delete-with-code, no assertion weakened)** — deleted store-backed
  `resolve_allitems_test.go`, the store tests + helpers in `resolve_exactmatch_test.go`
  (kept the JSONL ByIDFromJSONLExact security tests — SAME prefix-collision guarantee),
  the two store-fallback tests in `resolve_jsonl_test.go`, the three
  `TestStore_Resolve_*` in `pkg/storetest/lifecycle_test.go`, and
  `cmd/rd/byid_store_test.go` (whole file tested the deleted store fallback).
  Equivalent exact/prefix/ambiguous/all-items coverage survives on the JSONL spine.

Verify S2: `go build ./...` + `go vet ./...` GREEN. `go test ./pkg/resolve ./pkg/storetest
./pkg/state ./pkg/sync ./cmd/rd` GREEN. SDK-importing files 62 -> 57.

**Note on store.db creation:** read-only commands (list/ready/gates/work/focus/
pending) still call `openStore()` and pass the (now-ignored) store to the read
helpers, so `.cf/store.db` is still touched on reads. Eliminating that + dropping the
store param entirely is the ~40-call-site signature cascade deferred to the write-path
cut / step 5. `list.go` label-hint (`labelRegistryForHint`) still needs a real store
via `storederive.AllFromStore`, so it cannot drop `openStore()` until the label
registry moves to the nostr/JSONL projection.

---

## REMAINING (by area, biggest first)

Current SDK subpkg import counts (import-site lines, after S2):
store 34 · protocol 23 · cf-convention 16 · pkg/identity 15 · message 8 ·
pkg/naming 6 · transport/fs 5 · encoding 5 · cf-protocol/campfire 5 ·
authority/trust 5 · beacon 3 · pkg/provenance 2 · authority/provenance 2 ·
transport/http 1 · admission 1 · cf-discovery 1 · convention-extension/delegation 1.
(SDK-importing .go files: 57.)

STEP 2 STATUS: store-backed READ path DONE (S2b). What remains of the store is now
entirely the WRITE + init/join/server path (step 3) plus `pkg/storederive` (the one
shim) and the read-only-command `openStore()` calls that still create store.db.

---

## DONE (session 3) — SDK files 57 -> 55; read-only commands stop creating store.db

**S3a — drop the vestigial `store.Store` param from the read helpers (commit this
session).** After S2b the three read helpers (`allItemsFromJSONLOrStore`,
`byIDFromJSONLOrStore`, `byIDFromJSONLOrStoreExact` in root.go) ignored their store
param (`_ store.Store`) — resolution is nostr-projection-then-JSONL. Removed the param
entirely via signature change + mechanical rewrite of ALL ~40 call sites (write
commands keep their own `s` for the executor; they just stop passing it to the read
helper). Also dropped the store param from `publishImplicitUnblockNostr` (nostr.go),
which only fed `byIDFromJSONLOrStore`. Zero behaviour change — the param was already
unused for resolution.

**S3b — read-only commands no longer open `store.db` (step-5 outcome, partial).** With
the param gone, six read-only commands were opening a store purely for a now-dead
`defer s.Close()`: removed the `openStore()` blocks in `cmd/rd/{gates,focus,work,
pending}.go` and the two `rd nostr export`/`rd nostr parity` subcommands
(`cmd/rd/nostr.go`), and removed the conditional `openStore()` in `cmd/rd/show.go`.
`nostr.go` and `show.go` dropped their `store` import (SDK files 57 -> 55). These
commands now provision NO `.cf/store.db` on a read. Verified: `go build ./...` +
`go vet ./cmd/rd` clean, `go test ./cmd/rd` GREEN (78s).

  Read-only commands that STILL open a store (genuinely need it, deferred):
  `list.go` + `ready.go` (label-registry hint via `storederive.AllFromStore`),
  `dep.go` (cross-campfire membership scan), `playbook.go` (template), plus the
  write/init paths. The global `assertNoCampfireStore` invariant cannot tighten until
  the label registry moves to the nostr/JSONL projection AND the write path is cut.

## DONE (session 4) — OFFLINE RULING executed; init.go de-campfired

**S4a — `rd init` is nostr-only (this session).** Rewrote `cmd/rd/init.go` down to
the nostr-native surface: kept `initCmd` (nostr-only RunE), `initNostr`,
`ensureRelayConfig`, and a 2-flag `init()` (`--name`, `--description`). DELETED the
retired campfire/offline init surface entirely:
- `initCampfire` (~280-line legacy campfire-creating body) + the hidden `--campfire`
  flag,
- `initOffline` + the `--offline` flag (OFFLINE RULING: nostr-native local log
  subsumes offline — a nostr project with no reachable relay IS offline),
- `initJoin`, `parseBeaconString`, `writeJoinedProjectFiles`, `createRelayCampfire`,
  `resolveRelayURL`, `createLocalCampfire`, `isRemoteTransport`, `campfireTagsFromEnv`,
  `evaluateCampfireDurability`, `validateLocalBeaconRoot`, `registerProjectName`.
- init.go now imports NO campfire SDK (was: admission, campfire, cfencoding, protocol,
  store, transport/{fs,http}, beacon, identity, naming). SDK-importing .go files 55 → 54.
- `localCampfireBaseDir` (was in init.go, still called by join.go's vestigial campfire
  path) MOVED to join.go verbatim so join.go keeps building. join.go's campfire path is
  cut in the write-path session.

**Tests (delete-with-code, no assertion weakened):**
- Deleted `cmd/rd/{init_offline_test.go, init_join_idempotent_test.go,
  init_durability_test.go}` — they tested the now-deleted campfire/offline init
  functions directly.
- Extracted the shared `isolateTempDir(t)` helper (was defined in
  init_offline_test.go, used by type_alias_test.go + validate_flags_test.go) into new
  `cmd/rd/testhelpers_test.go`.
- Deleted campfire-org e2e that init via `rd init --campfire` (retired flag):
  `test/e2e/{offline_test.go, join_autosync_test.go, org_observer_test.go,
  upgrade_test.go, topology_test.go}`. The network-gated Topology4 skip dies with
  topology_test.go. nostr-native lifecycle coverage survives in
  `test/e2e/{init_test.go, init_nostr_test.go}` (create/claim/close on the default path).

Verify S4: `go build ./...` GREEN. `go vet ./cmd/rd` clean. `go test ./cmd/rd`
GREEN (81s, full package). `go test ./test/e2e -run Init_Nostr*` GREEN. e2e package
compiles.

REMAINING after S4 (the big write-path + harness session): convert the 16 write
commands (create/claim/close/update/delegate/gate/approve/reject/dep/label/engage/
playbook/complete + aliases done/fail/cancel/defer/progress) to nostr-only by deleting
their `withAgentAndStore`/`executeConventionOp` fallback branches (add nostr intercepts
to the aliases + playbook, which currently have NONE and so still hit the campfire path);
then delete the now-unused hub helpers in root.go (openStore/requireAgentAndStore/
withAgentAndStore/requireExecutor/rdLevelSource/requireClient/centerAuthorize/
requireConventionServer) and send.go (executeConventionOp*/sendPrebuiltMessage/
buildFlusher/bufferToPending/sendToProjectCampfire); cut join.go's campfire path
(keeping joinViaNostrInviteToken; note resolveName/campfireReader/isHex are also used by
revoke/kill/sessions/nostr_grant — those files go in the same sweep); cut sync.go's
autoSyncPull/clientLister (called by list/ready/show — remove those call sites) once
join.go stops using clientLister; delete pkg/conventionserver, pkg/provenance
StoreChecker, pkg/storederive, pkg/storetest, pkg/sync/inbound.go campfire pull; then
MIGRATE the NewEnv e2e harness (harness_test.go) from `cf init`/`cf create`/.campfire/root
onto a nostr `rd init` substrate so agent/attention/dep/gate/lifecycle e2e keep passing
(they currently exercise the CAMPFIRE write path via NewEnv); delete the remaining
campfire-only e2e (center_adoption/cfhome/recovery + newCampfireProjectDir); finally
go.mod/go.sum tidy + global no-store.db/no-.cf invariant + zero `campfire-net/campfire`
grep.

## PRIOR BLOCKER (now RESOLVED by OFFLINE RULING) — fate of `--offline` JSONL-only write path

The remaining bulk (step 3: delete `executeConventionOp`/`withAgentAndStore`/
`requireExecutor`/`send.go` campfire branch/`pkg/identity`/`pkg/provenance`/store) is
gated on ONE product decision. On a write command, `nostrNativeProject()` intercepts
EARLY and routes to `runXNostr` (secp256k1, local nostr-log). The campfire
`executeConventionOp` path below the intercept serves TWO remaining callers:
  (a) campfire-backed projects — clearly deleted by this item; and
  (b) `--offline` JSONL-only projects (`rd init --offline`) — whose write path is
      `withAgentAndStore` -> `requireAgentAndStore` -> `identity.Load(.cf/identity.json)`
      (ed25519) -> `message.NewMessage` -> `mutations.jsonl`. This path REQUIRES a .cf
      ed25519 identity + `message` — exactly the artifacts the spec END STATE forbids.
`initOffline` itself is SDK-free and writes only `.ready/project.json` (its init tests
stay), but the offline WRITE path is 100% SDK. So deleting `pkg/identity`/store forces
a decision on `--offline`:
  1. RETIRE `--offline` — nostr-native subsumes it (nostrwrite.go already treats the
     local signed-event log as the primary durable write; a nostr-native project with
     no reachable relay IS offline). Delete `initOffline`, the `--offline` flag, the
     offline write path, and the offline e2e write tests. SIMPLEST, matches spec
     end-state, but removes a documented public flag.
  2. REBUILD offline on secp256k1 — make `rd init --offline` pin a LOCAL (relay-less)
     board coordinate so `nostrNativeProject()` matches and offline uses the nostr
     write path. Preserves the `--offline` UX; needs a "local board" notion + detection
     change.
  3. Keep ed25519 offline as an explicit exception — CONTRADICTS the spec (no
     pkg/identity, no .cf). Not recommended.
RECOMMENDATION: option 1 (retire `--offline`; nostr-native local-log is the offline
mode). Needs owner/orchestrator sign-off because it drops a public flag. Once decided,
step 3 (write-path + campfire-server + init/join/sync deletion) and step 4 (campfire
e2e deletion, incl. migrating the campfire-`NewEnv` e2e tests gate/dep/lifecycle/agent/
attention onto a nostr harness) and step 5 (go.mod tidy, global no-store.db invariant)
can proceed.

### Step 2 — delete store-backed read path (unthread `store.Store` from reads)
- `pkg/state/state.go`: delete `DeriveFromStore`/`DeriveAllFromStore` +
  `msgrecsFromStore` -> state.go becomes fully SDK-free.
- Callers to repoint to JSONL/nostr (DeriveFromJSONL* already exist):
  `cmd/rd/list.go` (labelRegistryForHint/printUnknownLabelHints take `store.Store`),
  `cmd/rd/label.go:132`, `cmd/rd/dep.go:295/429/441/475`, `cmd/rd/create.go:223`,
  `cmd/rd/engage.go:93`, `pkg/crossdep/crossdep.go:68/190`,
  `pkg/resolve/resolve.go:45/57/91/112/128`, `pkg/storetest/helpers.go:498/508`.
- `cmd/rd/root.go`: `allItemsFromJSONLOrStore(s store.Store)` -> store-free
  `allItems()`; `openStore()`/`requireAgentAndStore`/`withAgentAndStore` removal;
  `crossdep.ApplyBlocking(items, s, aliases)` -> store-free cross-dep (cross-campfire
  topology is campfire-only — resolve cross-project deps from JSONL/nostr or drop).
- `pkg/provenance/checker.go` (`NewStoreChecker`) is store-backed role provenance —
  delete with `requireExecutor`'s store branch; nostr authz is `cmd/rd/authz_nostr.go`.
- `pkg/storetest/helpers.go` is a store-backed test harness — delete WITH the
  store-backed tests that import it (`pkg/state/label_*_test.go`, cmd/rd byid/store
  tests, etc.). Delete-with-code, not skip.

### Step 3 — delete campfire write/init/join/server
- `cmd/rd/send.go`: `executeConventionOpToCampfire`, `sendPrebuiltMessage`,
  campfire `bufferToPending`, `store.MessageRecordFromMessage`, `store.NowNano`,
  the whole campfire send branch. Nostr write path is `cmd/rd/nostrwrite.go`.
- `cmd/rd/init.go`: delete hidden `--campfire` flag + `initCampfire` (~930-line body)
  and its SDK imports (admission/campfire/encoding/protocol/store/transport/{fs,http}/
  beacon/identity/naming). Keep nostr init.
- `cmd/rd/join.go`: delete open-campfire join + `resolveTransportDir` (caller is
  campfire). Keep rd1_ nostr invite/join (`cmd/rd/nostr_invite.go`).
- `cmd/rd/sync.go`: has store-backed pull/push — repoint to nostr sync (`pkg/sync/
  nostr*.go`) / delete campfire branch.
- `pkg/conventionserver/*` (server/gate/inbox_watcher): in-process campfire
  convention server — delete; `root.go` `requireConventionServer` too.
- `cmd/rd/root.go`: `requireClient`/`requireExecutor`/`centerAuthorize`/`CFHome`
  campfire cascade / `projectRoot` campfire branch.
- Misc protocol/identity/naming holdouts: `cmd/rd/{revoke,kill,sessions,show,
  delegation_grant,gate,approve,create,ready,reject,playbook,label,dep,nostr,
  validate_flags}.go`, `cmd/migrate/ready-import/main.go`, `pkg/crossdep`,
  `pkg/resolve`. Most use `pkg/identity` (ed25519) — replace with the nostr actor
  key (secp256k1) already used by nostrwrite; or delete if only feeding campfire.

### Step 4 — delete campfire-vestigial e2e tests (delete-with-code)
`test/e2e/{center_adoption,cfhome,org_observer,topology,upgrade,join_autosync,
recovery}_test.go` + `harness_test.go` `newCampfireProjectDir` + any `cf admit`/
`cf create` harness usage. The network-gated Topology4 skip dies with topology_test.
KEEP nostr-native e2e (`init_nostr`, gate, migrate, invite rd1_, parity).
Also delete: `cmd/rd/{cfhome,help_campfire}_test.go`, campfire-only tests in
`cmd/rd` / `pkg/*` that only pass via the SDK or storetest.

### Step 5 — final
- `go.mod`/`go.sum`: `go mod tidy` after last import removed; SDK line gone.
- Eliminate residual `.cf/store.db` touch (openStore StorePath) so NO store.db is
  ever created; tighten `assertNoDotCf`/`assertNoCampfireStore` to assert store.db
  absent GLOBALLY. Keep the no-identity.json invariant.
- `grep -rI 'campfire-net/campfire' --include=*.go` returns nothing (historical
  comments acceptable but prefer to scrub).
- Full suite green, zero skips: `go test ./... -timeout 600s`.

---

## DONE (session 5) — write-command campfire path DELETED; non-test SDK files 35→19

Added `errNotNostrProject()` helper (nostrwrite.go). Deleted the campfire
`withAgentAndStore`/`executeConventionOp`/`requireExecutor` fallback branch from EVERY
write command — a non-nostr dir now errors via errNotNostrProject (no campfire executor
remains). Converted:
- **Simple close/status cmds** (claim, close, complete, delegate, approve, gate, reject):
  campfire body removed; SDK imports dropped. complete.go dropped `completePayload`.
- **create.go**: withAgentAndStore block gone; SDK imports (store/identity/state/
  storederive) dropped; nostr path is the only path.
- **update.go**: campfire field/status/claim executor block gone → runUpdateNostr only.
- **aliases.go** (done/fail/cancel/defer/progress): FULLY REWRITTEN onto nostr helpers
  (had NO nostr intercept before — the gap noted in S4). done/fail/cancel→runCloseNostr;
  cancel --cascade closes descendants via runCloseNostr; defer→runUpdateNostr(eta);
  progress resolves via nostrResolveItem, appends context, runUpdateNostr(context).
- **label.go**: define→errNotNostrProject (nostr has freeform labels, no registry);
  list→seed-atom registry only (state.DeriveAll("",nil), store-free); propose/add/remove
  campfire closures stripped; SDK imports dropped.
- **dep.go**: add/remove campfire closures→errNotNostrProject; depTree now reads
  allItemsFromJSONLOrStore (store-free); deleted resolveAcrossCampfires/findBlockMessage/
  findBlockMessageJSONL/hasTagStr/blockPayload; SDK imports dropped.

### DESIGN DECISION (session 5) — playbook + engage surface DELETED
`rd playbook` (create/list/show) and `rd engage` were 100% campfire-store-backed
(templates stored as work:playbook-create campfire messages; NO nostr projection).
The in-code design record (engage.go: "I5/ready-9ac deletes the playbook surface")
sanctions deletion. Deleted cmd/rd/{playbook.go,engage.go,engage_test.go} and the
engage helpers (runEngageNostr/publishEngagedItemsNostr) from nostrwrite.go. **pkg/playbook
(SDK-free template lib) KEPT** — its own tests stay. NOTE: this drops a documented
feature (MEMORY: "playbooks are core Ready features"). If the owner wants it back, the
clean re-platform is a local `.ready/playbooks.jsonl` template store (separate item) —
flagged in the return escalation_note.

Verify S5: `go build ./...` GREEN. Non-test SDK-importing files 35 → 19 (remaining:
cmd/migrate, delegation_grant, join, kill, list, ready, revoke, root, send, sessions,
sync, validate_flags, pkg/{conventionserver×3, provenance, storederive, storetest,
sync/inbound}).

### REMAINING after S5 (the read + infra + declarations session)
- **Declarations subsystem** (validate_flags.go uses cf-convention `Declaration`;
  root.go loadDeclaration + convention.Parse; create.go ValidateEnumFlags). Enum
  validation must move off cf-convention OR loadDeclaration re-implemented natively.
- **send.go**: delete entirely (executeConventionOp*/sendToProjectCampfire/
  sendPrebuiltMessage/bufferToPending campfire) — now UNUSED by write cmds; check
  aliases/validate callers gone (they are). requireClient callers: ready/kill/revoke/
  show/sessions/sync/join/authz_nostr still call it.
- **root.go**: openStore (list/ready still call), requireAgentAndStore (join/nostrwrite?),
  withAgentAndStore (validate_flags?), requireExecutor, requireClient, requireConventionServer,
  centerAuthorize, CFHome cascade, rdLevelSource, PersistentPreRunE campfire branch.
- **list.go/ready.go**: still openStore + autoSyncPull; repoint reads to nostr projection
  (nostrReadActive already default). ready.go also requireClient.
- **show.go**: requireClient/autoSyncPull.
- **sync.go**: campfire autoSyncPull/clientLister branch → nostr sync only.
- **join.go**: delete open-campfire join + resolveTransportDir + clientLister; keep
  joinViaNostrInviteToken. (resolveName/campfireReader/isHex shared w/ revoke/kill/
  sessions/nostr_grant.)
- **revoke/kill/sessions/delegation_grant.go**: campfire admin cmds — convert to nostr
  (kind-39301 grant/revoke via nostr_grant.go / authz_nostr) or delete campfire branch.
- **pkg/conventionserver** (3 files): delete; root.go requireConventionServer too.
- **pkg/provenance/checker.go** (StoreChecker): delete w/ requireExecutor.
- **pkg/storederive**: delete once list/ready label-hint + dep off the store (label DONE;
  dep DONE; list/ready label-hint remain).
- **pkg/storetest**: delete harness + its store-backed tests (delete-with-code).
- **pkg/sync/inbound.go**: delete campfire store→JSONL pull.
- **cmd/migrate/ready-import/main.go**: campfire import tool — de-SDK or delete.
- **Tests**: many cmd/rd/*_test.go + pkg/* tests reference deleted campfire paths →
  delete-with-code or repoint to nostr. e2e campfire tests (center_adoption/cfhome/
  org_observer/topology/upgrade/join_autosync/recovery + harness newCampfireProjectDir).
- **go.mod/go.sum**: `go mod tidy` after last import; assert no store.db globally.

## CURRENT FAILURES / KNOWN RED (as of S5)
`go build ./...` (non-test) GREEN. The **cmd/rd TEST package is RED** (allowed
between sessions): test files still reference symbols deleted in S5
(completePayload, executeConventionOp, withAgentAndStore, requireExecutor,
findPlaybook, engageCmd, etc.). These tests must be deleted-with-code or repointed
onto the nostr write path in the next session (many just exercise the campfire
executor and should be deleted). Library pkgs pkg/state + pkg/playbook still pass.
Full suite NOT run (item incomplete).

## DONE (session 6) — declarations de-SDK, read-path de-store, admin de-campfire; non-test SDK files 19→10; cmd/rd TEST GREEN

Focus: the read + declarations + leaf-package + admin slice. `go build ./...`,
`go vet ./...` GREEN. `go test ./pkg/... ./cmd/...` GREEN (cmd/rd 80s, zero skips).
test/e2e compiles+runs. Non-test SDK-importing files 19 → 10.

**Declarations enum validation is now SDK-free.** Added native `declarations.EnumArgs(op)`
(pkg/declarations) returning ordered enum args from the embedded JSON — no cf-convention
parse. Rewrote `validate_flags.go`: `ValidateEnumFlags(operation string, flagValues)`
(was `ValidateEnumFlags(*convention.Declaration, ...)`). `create.go` calls
`ValidateEnumFlags("create", ...)` (dropped `loadDeclaration("create")`). Rewrote
validate_flags_test + type_alias_test callers; the synthetic-declaration test now proves
derivation via `declarations.EnumArgs`.

**Read commands are store-free.** `list.go`: dropped `openStore()`; `printUnknownLabelHints`
now takes only atoms and checks the seed registry via `state.DeriveAll("",nil)` (no store /
storederive). `ready.go`: `runReady(selfHex)` (dropped the `store.Store` param + openStore);
non-nostr branch now `errNotNostrProject()`. Both dropped `store`/`identity` imports.

**root.go hub trimmed.** DELETED `requireExecutor`, `rdLevelSource`, `loadDeclaration`,
`requireConventionServer`, `withAgentAndStore`; `PersistentPreRunE` no longer starts the
in-process convention server (nostr-native provisions no client/server). Imports dropped:
cfauthprov, cf-convention, cfprov, conventionserver, declarations, provenance, context.
KEPT (still used): requireClient/centerAuthorize (send/sync/join/show/sessions/authz),
requireAgentAndStore (join), openStore (none now — removed uses; still defined, used by
requireExecutor? no — now UNUSED, retained w/ store import for join's requireAgentAndStore).

**Admin cmds de-campfired.** `revoke.go` + `kill.go`: deleted the campfire executor tails
(and revoke's `findMembersAdmittedBy`/`capAdmitted`); non-nostr dir → `errNotNostrProject()`.
Retroactive revoke redirects to `rd nostr revoke --from`. Dropped protocol/delegation/
ed25519/hex/context/json imports.

**Leaf packages deleted:** `pkg/conventionserver` (3 files+tests) and `pkg/provenance`
(checker+test) — the nostr port is `pkg/sync/rolegrant.go`.

**send.go gutted to keepers.** Deleted ALL campfire send funcs (sendToProjectCampfire,
executeConventionOp, executeConventionOpToCampfire, buildFlusher, sendPrebuiltMessage,
bufferToPending, extractOperationFromTags) — they were test-only after S5. KEPT projectRoot,
formatCampfireIDForDisplay, minInt. send.go now imports ONLY `pkg/naming` (via projectRoot)
+ rdconfig — SDK surface 9→1.

**Test sweep (delete-with-code / repoint, no assertion weakened):**
- DELETED executor-path test files (tested deleted convention.Executor+loadDeclaration):
  label_test, label_sanitize_test, create_label_executor_test, min_operator_level_test,
  flag_validation_test, executor_validation_test, label_ops_executor_test, send_test,
  convention_server_test.
- REPOINTED: complete_test (dropped completePayload JSON-shape tests, kept reason-validation);
  revoke_test (dropped findMembersAdmittedBy/capAdmitted tests, kept pubkey-validation);
  dep_test (dropped TestHasTagStr — hasTagStr deleted); nostrwrite_test (dropped
  publishEngagedItemsNostr engage test + playbook import); label_rendering_test +
  ready.go/list.go printUnknownLabelHints call sites; validate_flags_test/type_alias_test
  to native ValidateEnumFlags("create",...).
- DELETED obsolete migration-era tests whose read path is removed by the cutover:
  `TestNostrDualRead_ListReadyShow_MatchDefault` (+ Group B helpers runJSONItems/
  runJSONShow/assertItemSetsMatch/flagSet — JSONL-default-vs-nostr parity; there is one
  read path now) and `TestCreate_UnknownLabel_E2E_ExitDemandNoWrite` (create.go's nostr
  path does NOT validate labels — freeform; the label-demand funcs are now orphaned).

### REMAINING after S6 (next sessions)
- **join.go** (SDK): cut open-campfire join + resolveTransportDir + clientLister; keep
  joinViaNostrInviteToken. Shares resolveName/campfireReader/isHex with revoke(now clean)/
  kill(clean)/sessions/nostr_grant.
- **sync.go** (SDK): campfire autoSyncPull/clientLister/campfireReadClient branch → nostr
  sync only (list/ready/show call autoSyncPull).
- **sessions.go** (SDK): campfire grant-holder read (activeGrantHolders/authorityResolver/
  scopeForKey/loadAuthorityResolver) still used by ready.go `--scope` + show.go `--audit`
  campfire branches → convert those to nostr (nostrAuthorityResolver already exists) or cut.
  NOTE: ready.go `--scope` now dead on nostr (projectRoot !ok → errors) — convert or drop flag.
- **delegation_grant.go** (SDK): campfire delegation grant cmd → nostr (nostr_grant.go) or cut.
- **root.go**: openStore now has NO callers (list/ready dropped it) — DELETE it + requireClient/
  centerAuthorize/requireAgentAndStore/CFHome cascade once join/sync/sessions/show stop using
  them. `store`/`identity`/`protocol` imports go with them.
- **pkg/storederive**: now used ONLY by tests (pkg/state/label_*_test, storetest/helpers,
  test/integration/lifecycle_test) — delete with those store-backed tests (repoint the label
  registry tests to JSONL derive).
- **pkg/storetest** + store-backed label tests (pkg/state/label_enforcement/ops/registry_test),
  **pkg/sync/inbound.go** (campfire store→JSONL pull + inbound_test), **cmd/migrate/ready-import**.
- **projectRoot**: still imports pkg/naming for campfire project-name alias resolution — strip
  Priority-1 naming block (keep .campfire/root legacy detect or make it always-false) to drop
  the last send.go SDK import.
- **e2e**: center_adoption/cfhome/recovery + harness newCampfireProjectDir (NewEnv campfire
  substrate) → migrate onto nostr `rd init`. test/integration is build-tag gated (uses storederive).
- **go.mod/go.sum tidy** + global no-store.db/no-.cf assert + zero `campfire-net/campfire` grep.

### DEAD-CODE NOTE (S6)
`appendLabelDemand`/`validateLabelsAgainstRegistry` (create-label demand) are now ORPHANED —
create.go's nostr path doesn't call them (freeform labels). Their unit tests still pass. Remove
with the label-demand feature in a later pass, or re-wire if the owner wants label-registry
enforcement back on the nostr path. dep_test's buildBlockArgsMap/buildUnblockArgsMap mirror
tests validate a deleted op's arg shape (self-contained, still green) — candidates for removal.

## DONE (session 7) — CAPSTONE COMPLETE: zero campfire SDK, full suite GREEN

Removed the campfire SDK from EVERY remaining non-test file, migrated the e2e
harness onto nostr, tidied go.mod, tightened the no-store.db invariant globally.
`grep -rI 'campfire-net/campfire' --include=*.go` returns NOTHING; go.mod/go.sum
carry no campfire line. `go build ./...` + `go vet ./...` (incl. -tags integration)
clean. FULL SUITE `go test ./... -timeout 600s` GREEN: 15 ok packages, 0 FAIL,
0 SKIP.

**root.go** — deleted openStore, requireAgentAndStore, IdentityPath, requireClient,
centerAuthorize, protocolClient var; dropped protocol/store/identity/isatty/bufio/
strings imports. Kept CFHome/RDHome/walk-ups/readyProjectDir/jsonlPath + the
store-free read helpers.
**send.go** — projectRoot + formatCampfireIDForDisplay stripped of the naming
alias store (last SDK import); projectRoot now only does legacy .campfire/root
hex detection, formatCampfireIDForDisplay returns hex as-is. minInt kept.
**sync.go** — deleted campfireReadClient/clientLister/autoSyncPull; dropped
protocol/store/rdSync imports; syncCmd still proxies to nostrSyncCmd. Removed the
autoSyncPull() call in list.go/show.go/ready.go.
**join.go** — rewrote to nostr-only: RunE handles --reset-beacon-root + rd1_ token
(joinViaNostrInviteToken) and errors on anything else; kept resetBeaconRoot
(rdconfig) + isHex (shared). Deleted resolveName/resolveTransportDir/
localCampfireBaseDir/pollForRoleGrant/campfireReader/containsTag/grantTargets/
bootstrapJoinedProject/validate+applyBeaconRootTOFU/validateNameFormat/isInteractive.
**sessions.go** — rewrote to nostr-only: sessionsCmd→runSessionsNostr;
NEW nostrScopeForKey (ready --scope now derives claim-authority from kind-39301
grants); kept auditLabeler + nostrAuthorityResolver + loadNostrAuthorityResolver +
shortKey. Deleted grantHolder/activeGrantHolders/messagesOf/scopeClient/scopeForKey/
opPatternCovers/authorityResolver/newAuthorityResolver/loadAuthorityResolver/
identityRevokedTag + the campfire RunE branch. ready.go --scope → nostrScopeForKey;
show.go --audit dropped the campfire requireClient/loadAuthorityResolver else-branch.
**Deleted files:** delegation_grant.go, pkg/storederive, pkg/storetest (harness +
all its store-backed tests), pkg/sync/inbound.go(+test), cmd/migrate/ready-import
(campfire replay tool — superseded by `rd nostr migrate`), test/integration
(campfire Go-API tests).
**e2e harness (harness_test.go)** — MIGRATED NewEnv from `cf init`/`cf create`/
.campfire/root onto the nostr-native default `rd init --name` with hermetic
HOME/RD_HOME/CF_HOME + unreachable relay. New Env fields Home/RDHome/Board/Owner;
IdentityPubKeyHex returns the board owner (secp256k1 self). Deleted
newCampfireProjectDir + memberPubKeyHex; rewrote TestHarness_EnvCreates. Deleted
campfire-only e2e: center_adoption/cfhome/recovery. Repointed
TestE2E_Dep_Add_CrossProjectSyntax to the post-cutover "not found / not supported"
contract (cross-campfire deps deleted).
**Test sweep (delete-with-code / repoint, no assertion weakened):** deleted
root_test.go (requireClient/requireAgentAndStore/protocolClient), sessions_test/
scope_test/audit_test/fake_client_test/sync_test/auto_sync_test/delegation_grant_test
(campfire executor+resolvers — nostr equivalents covered by authz_nostr_test +
nostrwrite_test show-audit), pkg/state/label_{ops,enforcement,registry}_test +
pkg/playbook/labels_integrate_test (storetest harness). Trimmed join_test to
isHex+resetBeaconRoot keepers; display_test to the SDK-free formatCampfireIDForDisplay
fallback tests; migrate_test/nostr_test/nostrwrite_test off openStore/requireClient;
rdconfig config_test dropped the naming-alias-store test.
**Invariant tightened:** walkAssertNoCampfireIdentity now fails on store.db
anywhere (not just the show path) — no rd command provisions a campfire store now.
**go.mod tidy** — campfire v0.32.0 line removed from go.mod + go.sum.

REMAINING: NONE. Item complete.

## FILE-BY-FILE STATUS
- pkg/msgrec/msgrec.go .............. DONE (new, SDK-free)
- pkg/state/state.go ................ ENGINE DONE; 2 store shims remain (step 2)
- pkg/state/*_test.go (11) .......... DONE (repointed to msgrec)
- pkg/state/label_*_test.go ......... store-backed via storetest — dies in step 2
- pkg/jsonl/types.go, reader_test.go  DONE (SDK-free)
- pkg/sync/inbound.go ............... boundary shimmed; store import remains (step 3)
- everything else .................. TODO per steps above
