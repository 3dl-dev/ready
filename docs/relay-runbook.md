# Self-Hosted strfry Relay Runbook (ready-efe)

Reproducible-from-scratch runbook for the two LAN strfry (nostr) relays that
back the rd→nostr migration, plus (ready-1ab) the one PUBLIC relay dual-published
alongside them so a browser can reach the board. See "The public relay
(wss://relay.3dl.network)" below for why there are three endpoints, not two.

> **Invariant:** The relays are a **CACHE / always-available copy, NEVER the
> source of truth.** The source of truth is each project's local authoritative
> log (`.ready/nostr-log.jsonl`). The relays exist so other machines can
> read/write events without a hardcoded hub, and so events survive a single
> relay going offline. Treat relay data as reconstructible at any time from the
> authoritative source.

## Topology

| Role       | VMID | Host                | Endpoint                        | Reachable from | Why it exists |
|------------|------|----------------------|----------------------------------|-----------------|----------------|
| relay-a    | 210  | mainframe (Proxmox)  | `ws://relay-a.internal:7777`     | LAN only        | fast local CLI sync — a dumb pipe, no TLS to operate |
| relay-b    | 211  | mainframe (Proxmox)  | `ws://relay-b.internal:7777`     | LAN only        | same as relay-a; second copy for availability |
| public     | —    | Azure Container Apps | `wss://relay.3dl.network`        | anywhere (browser-openable) | the ONLY endpoint a browser board page can open — see below |

Both LAN VMs: Ubuntu 24.04 (Proxmox template 9000), 2 cores / 4 GB / 20 GB disk,
`--onboot 1` (survive host reboots). strfry built from source (`v1-b80cda3`),
run as a systemd service `strfry.service` under user `baron`, listening on
`0.0.0.0:7777`.

The LAN relays live on the **mainframe Proxmox hypervisor**, not on any workshop /
migration VM, so they persist across the whole migration.

## Why two LAN relays

Multi-relay topology gives availability: if either relay is offline, the other
still serves the full event set. They are kept reconciled with relay-to-relay
Negentropy (`strfry sync`, NIP-77). This is proven live by `scripts/relay-demo.sh`
(step 3 takes each relay offline in turn and reads back from the survivor;
step 5 reconciles an event A→B via Negentropy).

## The public relay (wss://relay.3dl.network) and dual-publish (ready-1ab)

**Why it exists:** a browser tab serving the board over `https://` refuses to
open an insecure `ws://` socket at all (mixed content, no click-through), and
the two LAN relays above are also RFC1918 addresses unreachable from outside
baron's network regardless of scheme. Neither problem is fixable by adding TLS
to the LAN boxes alone — an https page still can't dial an address it can't
route to. `ready-1ab` ruled against retrofitting TLS onto the LAN strfry
processes (options considered: DNS-01 ACME for the LAN IPs, a reverse proxy, an
internal CA — see that item's history) and instead reused an **already-running**
production relay:

- **What it is:** the same nostr relay software as the LAN boxes' underlying
  protocol, but a different implementation (`github.com/3dl-dev/nostr-relay`),
  deployed as an Azure Container App with a browser-trusted managed TLS
  certificate. `relay.moot.pub` is the SAME container app under a second custom
  domain — not a second relay, don't count it as one.
- **Scale-to-zero is deliberate:** `minReplicas=0`, `maxReplicas=1`
  (`nostr-relay/infra/prod.bicep`) — an idle relay costs nothing, at the price
  of a cold-start delay (~12s observed) on the first request after idle. The
  board client must tolerate a connecting/retry state on cold start; this is
  not a bug to "fix" by raising `minReplicas`.
- **It enforces its own tenant write-allowlist** (`ready-e95`) — unlike the LAN
  relays (`ready-5fd`, dumb pipe, no relay-side gate), the public relay rejects
  writes from pubkeys its operator hasn't admitted, because rd is a TENANT on
  infrastructure it does not own, not the owner of it. Reads are unrestricted.
  This is orthogonal to rd's own app-side trust gate (`ready-d53`) — both must
  admit a pubkey for that pubkey's writes to reach the board over this relay.

**Dual-publish, not cutover:** `relay_endpoints` carries all three entries with
`read`+`write` true (see current shape below); `relayPublish`
(`pkg/sync/nostroutbound.go`) writes every event to every write relay,
best-effort, independently. Nothing about the LAN relays changed — they are
not a migration-era stand-in being phased out. **The two are permanent by
design, not a temporary dual state**: the LAN relays stay plain `ws://` because
that is what makes them fast and local for the CLI's own sync path (`rd`
itself dials `ws://` fine — it isn't a browser), and the public relay stays the
one browser-reachable endpoint. There is no retirement trigger for the plain-ws
LAN listeners because retiring them would remove the CLI's fast local sync path
with no replacement, not complete a migration. What IS finished migrating: `rd
board`'s emitted link now filters to only the browser-openable relay
(`ready-634`, `browserOpenableRelays()` in `cmd/rd/board.go`) — the CLI still
dials all three from `rd.json`.

## Updating relay_endpoints on an existing machine

`relay_endpoints` lives in **local, un-synced config** — `~/.config/rd/rd.json`
(machine-wide default, `$RD_HOME`). But `resolveRelayConfig` (`cmd/rd/nostr.go`)
walks UP from cwd first, and **either of two project-level files can shadow the
home default entirely, stopping the walk before it ever reaches `rd.json`**:

- a project's own `.ready/config.json` (machine-local, set at `rd init` time or
  later — never committed), or
- the project's **COMMITTED** `.ready/board.json` (ready-f12 — the one `.ready/*`
  file tracked by git, so a fresh clone carries whatever relay policy was
  committed there with no `rd init` step at all).

At a given directory, a declaring `.ready/config.json` wins over that level's
`.ready/board.json`; either one wins over the home file. **This means editing
`~/.config/rd/rd.json` on a project that already carries `relay_endpoints` in
`.ready/board.json` or `.ready/config.json` changes nothing for that project** —
check which file actually governs before editing:

```bash
cat .ready/board.json 2>/dev/null    # committed & shared — if relay_endpoints is set
                                       # here, THIS is what to edit (and commit), not rd.json
cat .ready/config.json 2>/dev/null   # machine-local override for this project only —
                                       # if set (or relays_local_only:true), edit this instead
```

- **Neither file declares a policy (or neither exists) at any ancestor
  directory:** the project inherits the home default — edit `~/.config/rd/rd.json`
  below.
- **`.ready/board.json` declares `relay_endpoints`:** edit and commit that file
  instead (`rd init`, `rd link`, or `rd follow` is what wrote it) — the change is shared, not
  per-machine, so every clone (not just this one) needs the new entry.
- **`.ready/config.json` declares `relay_endpoints`** (or `relays_local_only:
  true`): edit that file — it is a machine-local override for this project
  alone; the home `rd.json` edit below will not reach this project until that
  override is removed or updated too.

For the plain "no project-level override, just the home default" case:

```bash
# Back up first — there is no automated rollback for this edit (ready-199).
cp ~/.config/rd/rd.json ~/.config/rd/rd.json.bak-"$(date +%s)"

# Add the public relay alongside whatever LAN/other entries are already there.
# Do not remove the existing entries — see "Dual-publish, not cutover" above.
```

```json
{
  "relay_endpoints": [
    { "url": "ws://relay-a.internal:7777", "read": true, "write": true },
    { "url": "ws://relay-b.internal:7777", "read": true, "write": true },
    { "url": "wss://relay.3dl.network",    "read": true, "write": true }
  ]
}
```

Verify with a real round-trip, not just that the file parses:

```bash
rd status                 # read/write reachability for the linked board
rd relay audit --relay wss://relay.3dl.network   # independent read-back from that relay alone
```

A machine with no `rd.json` relay policy at all is **local-only by default**
(`rdconfig.DefaultRelays()` returns none — see "Endpoint config for rd" below):
it never had LAN relays either, so this same edit is also how a brand-new
machine opts in to the shared board rather than staying standalone.

## Provisioning (from scratch)

Provisioning is codified in the **mainframe repo** (a cross-repo ops artifact,
not in this repo):

- `mainframe/scripts/mk-relay.sh` — clones template 9000, sets a static IP,
  2 cores / 4 GB / 20 GB, `--onboot 1`, builds strfry from source over SSH,
  writes `/etc/strfry.conf`, installs the systemd unit, starts the service.
- `mainframe/cloud-init/relay-vendor.yaml` — cloud-init that installs the strfry
  build dependencies + `qemu-guest-agent`.

Run on the Proxmox host (`ssh root@mainframe.stealth.baron.local`):

```bash
cd /root/mainframe
bash scripts/mk-relay.sh 210 relay-a relay-a.internal
bash scripts/mk-relay.sh 211 relay-b relay-b.internal
```

`mk-relay.sh` authorizes three SSH keys on each VM: `baron.pub` (operator),
`proxmox-root.pub` (so the host-run build step can SSH in), and `workshop.pub`
(so the demo/proof, which runs from the workshop VM, can stop/start strfry and
run `strfry sync`).

### Manual build (what mk-relay.sh automates)

On a fresh VM with build deps installed:

```bash
sudo git clone --depth 1 https://github.com/hoytech/strfry /opt/strfry
sudo chown -R baron:baron /opt/strfry
cd /opt/strfry
git submodule update --init
make setup-golpe
make -j"$(nproc)"
sudo cp strfry /usr/local/bin/strfry
```

Build deps (Ubuntu 24.04): `build-essential g++ make pkg-config libtool
libssl-dev zlib1g-dev liblmdb-dev libflatbuffers-dev libsecp256k1-dev
libzstd-dev git`.

## strfry.conf (key settings)

Based on the shipped `strfry.conf` with these overrides:

```
db = "/var/lib/strfry/db/"

relay {
    bind = "0.0.0.0"
    port = 7777

    auth {
        # NIP-42 AUTH is offered but is NOT the write gate. The write-allowlist
        # is enforced by the writePolicy plugin below, keyed on the event's
        # signed author pubkey (see "Write-allowlist" section).
        enabled = true
        serviceUrl = "ws://<this-relay-ip>:7777"
    }

    writePolicy {
        # ready-266: only ADMITTED portfolio pubkeys may WRITE. Reads stay open.
        plugin = "/etc/strfry/write-allowlist.py"
    }

    info { name = "relay-a" }   # or relay-b

    negentropy {
        # NIP-77 — native. Required for 'strfry sync' relay-to-relay reconcile.
        enabled = true
    }
}
```

- **Negentropy (NIP-77):** enabled by default in strfry; required for the
  relay-to-relay sync below and for later rd sync work.
- **NIP-42 auth:** offered, but not the write gate — see the write-allowlist
  below.

## Write-allowlist (ready-266)

> **This whole section is OPTIONAL and advanced.** It only applies to
> operators who run their OWN locked relay (like relay-a/relay-b below).
> `rd grant <pubkey>` alone is **not required** to touch this section — it
> authorizes the grantee on **any public relay**, because rd enforces
> write-authz app-side (reconcile drops any author outside the fail-closed
> trusted set = self + signed kind-39301 grants). Inviting a teammate is
> `rd grant <pubkeyHex>` and nothing else; `rd relay sync-allowlist` is an
> extra step that only matters if you additionally lock down your own relay's
> write path at the transport layer, in addition to rd's app-side gate.

Both relays are LOCKED so that only ADMITTED portfolio identities may **write**
(publish events). **Reads stay open** — strfry's `writePolicy` governs only the
write path, so the relays remain a public read cache.

**Enforcement is by the event's author pubkey — no NIP-42 AUTH challenge.** strfry
validates every event's id + BIP-340 schnorr signature *before* invoking the
writePolicy plugin, so when the plugin sees `event.pubkey == P` strfry has already
proven the event was signed by P's secret-key holder. An attacker cannot forge an
allowlisted author, so checking `event.pubkey` against the allowlist *is* the
enforcement. A NIP-42 AUTH challenge would add nothing for signed-event write
control, so the rd client publish path is unchanged. This is **defence-in-depth on
top of** rd's client-side web-of-trust ingestion gate (ready-d53) — neither layer
replaces the other.

### Components (version-controlled in the `ready` repo)

| Artifact | Installed to (on each relay) | Purpose |
|----------|------------------------------|---------|
| `scripts/relay-policy/write-allowlist.py`   | `/etc/strfry/write-allowlist.py`   | the writePolicy plugin |
| `scripts/relay-policy/write-allowlist.json` | `/etc/strfry/write-allowlist.json` | the admitted-pubkey allowlist (SOURCE OF TRUTH) |
| `scripts/lock-relays.sh` | — (run from workshop VM) | installs both + sets `writePolicy.plugin` + restarts strfry, idempotently |

The plugin reads one JSON request per line from strfry and answers `accept` for
allowlisted authors, `reject` otherwise. It **re-reads the allowlist whenever the
file mtime changes**, so admitting a pubkey takes effect without restarting
strfry. It **fails closed**: a missing/malformed/empty allowlist rejects every
write (an absent list means "trust nobody", never "trust everybody").

### The allowlist — single source of truth

`scripts/relay-policy/write-allowlist.json` is a JSON object mapping each admitted
lowercase-hex x-only pubkey to a human label:

```json
{
  "48ea98a915f44a28810c33c017c43dc7d5595f3541522c3bc8c90327ec9df497": "machine-2 rd-node portfolio key (node-2.internal, $RD_HOME/nostr-identity.json)",
  "6c74c7bb0f0acb9ee4820f63b52f4209490eaef6fba7d1d2c34c2622413498f1": "dontguess exchange operator key (node-2.internal ~/.dontguess/nostr-operator.key)",
  "a9f766ae56bbf466d2d361e5b1788b7cd689fd8e3b418e35b002b313f478db25": "workshop VM portfolio key (machine-1, $RD_HOME/nostr-identity.json)"
}
```

The committed file above is **reconciled byte-for-byte against the live relays**
(ready-5ae): P1 (owner `a9f766ae`) and P2 (maintainer `48ea98a9`) are now covered by
explicit owner-signed kind-39301 role-grants, so the admitted set is **purely
grant-derived** for our web-of-trust keys — no longer preserved-by-absence. The third
entry, the `dontguess` exchange operator key `6c74c7bb`, is **another project's
identity** sharing the relay: it is a documented **preserved/unmanaged** tenant, NOT
granted into rd's role-grants. **Decision (ready-677): leave it preserved, do NOT
role-grant it.** dontguess authors no `ready` work-items — it only shares the strfry
relay infrastructure — so a `ready` `contributor`/`maintainer` grant would wrongly
authorize it to mutate `ready`'s board state. A shared-infra tenant belongs in the
no-lockout preserve set, not in `ready`'s web-of-trust. It is present in the committed
file and on both live relays; `sync-allowlist` preserves it and never locks it out.

This is the SAME trust set as rd's **client-side** gate: `rdconfig.Config`
`TrustedPubkeys` + the self portfolio pubkey (`TrustSet`, ready-d53). Keep them
consistent — *an identity that may write to the relay is exactly an identity whose
events rd will ingest*. On each machine, `$RD_HOME/rd.json` `trusted_pubkeys` lists
the OTHER admitted machines (self is implicit); the relay allowlist lists them ALL
(there is no implicit self on a relay). The pubkeys are the `pubkey_hex` in each
machine's `$RD_HOME/nostr-identity.json` (materialized by rd,
`LoadOrCreatePortfolioKey`).

### Admitting / revoking a pubkey — signed source (ready-84e / BP-5)

The hand-edit workflow below is **superseded** by `rd grant` / `rd nostr
revoke` + `rd relay sync-allowlist`, which regenerate the allowlist from **one signed
source** (the kind-39301 role-grants) instead of two hand-kept lists. This ends the
drift this runbook warns about: the client trust set and the relay file now derive
from the same signed log (design `docs/design/nostr-identity-model.md` §4/§6, A3).

```bash
# One signed act admits an actor across BOTH the client trust set and the relay.
# role is OPTIONAL — omit it and it defaults to contributor:
rd grant <pubkeyHex> --label "machine-3 rd-node"                    # owner-signed 39301
rd relay sync-allowlist                                              # OPTIONAL, advanced — DRY RUN: prints the diff
rd relay sync-allowlist --apply                                     # OPTIONAL, advanced — writes the file + scp/ssh to both relays

# Revoke (prospective by default — past authoritative events stay honored):
rd revoke <pubkeyHex>
rd relay sync-allowlist --apply
```

`sync-allowlist` derives the admitted set = `{ board author } ∪ { non-revoked
grantees }`, and — the safety property against the LIVE locked relays — **removes a
currently-admitted key IFF it has an explicit `role=revoked` grant**. A
currently-admitted key with **no** rd grant (e.g. a third-party tenant sharing the
relay, such as the `dontguess` exchange operator key) is **PRESERVED** and reported,
never silently dropped; if a key would be removed *without* a revoke grant the apply
path **fails closed**. `--dry-run` (the default, i.e. omit `--apply`) prints the
added/removed/preserved diff for review before anyone is removed.

Only the **board author (owner)** may grant `maintainer`/`owner` (the escalation
cap); a maintainer may grant only `contributor`/`revoked`. The board must be pinned
first with `rd link` (writes `SyncConfig.Board = 30301:<owner>:<boardD>`
to `.ready/config.json`).

**Admitting an invited teammate (self-mint claim model, ready-ce0).** `rd invite`
mints a TTL-bounded, one-use **claim-nonce** (the `rd1_` token carries **no key**).
The recipient runs `rd join <token>`, which **self-mints** a key and joins
**read-only** (writing nothing to the relays), then reports its `pubkey` and the
`claim`. Admit it by binding the claim to that pubkey — this is the same one signed
act, plus the `--claim` flag:

```bash
rd grant <joiner-pubkey> --claim <claim-nonce>   # role optional (defaults to contributor); binds nonce → pubkey
rd relay sync-allowlist --apply                  # OPTIONAL, advanced — only if this relay is locked
```

**Single-use is owner-enforced:** a claim-nonce binds to **exactly one** pubkey.
If a leaked token is joined by two people, only the **first** pubkey you `--claim`
is admitted; a second `rd grant ... --claim <same-nonce>` for a different pubkey is
**refused** (the second joiner needs a fresh `rd invite`).

**Manual fallback** (still valid; `sync-allowlist` produces the same file format):

1. Add `"pubkey": "label"` to `scripts/relay-policy/write-allowlist.json`.
2. Add the pubkey to every *other* machine's `$RD_HOME/rd.json` `trusted_pubkeys`
   (client-side ingestion gate).
3. Re-run `scripts/lock-relays.sh` (idempotent) to push the updated allowlist to
   both relays. No strfry restart is needed for an allowlist-only change — the
   plugin reloads on mtime change — but `lock-relays.sh` restarts anyway to also
   reconcile config/plugin drift.

### Locking the relays from scratch

```bash
# From the workshop VM (needs ssh baron@<relay> + passwordless sudo on the relays):
scripts/lock-relays.sh                 # both relays (default)
scripts/lock-relays.sh relay-a.internal    # a single relay
```

`mk-relay.sh` (mainframe repo) also installs the plugin + a seed allowlist at
provision time, so a freshly built relay is locked from first boot.

### Proof (ground-source, no mocks)

`scripts/relay-writepolicy-demo.sh` proves, on relay-a AND relay-b, that (a) the
allowlisted portfolio key publishes and is ACCEPTED, (b) a random untrusted key is
REJECTED with the relay's own block reason, and (c) reads stay open. Captured
output: `docs/relay-writepolicy-demo-output.txt`.

`scripts/nostr-grant-revoke-demo.sh` proves the full BP-5 loop against the live
locked relays: a fresh agent key is (b) REJECTED, then `rd grant` +
`sync-allowlist --apply` makes its write (d) LAND, then `rd revoke` +
`sync-allowlist --apply` makes it (f) REJECTED again — while the owner (P1),
machine-2 (P2), and the unmanaged `dontguess` tenant key stay admitted throughout.
The script captures the original allowlists and restores them on exit. Captured
output: `docs/nostr-grant-revoke-demo-output.txt`.

The Go live tests (`RD_NOSTR_LIVE_RELAY=1 go test ./pkg/sync/ ./pkg/nostr/`) sign
with the allowlisted key (`liveRelayKey`, resolved from
`$RD_HOME/nostr-identity.json` or `RD_NOSTR_TEST_SECRET_HEX`) and include
`TestLiveRelay_WriteAllowlistTrustGate`, which proves both the relay-side rejection
(ready-266) and the client-side drop (ready-d53) on both relays.

## systemd service

`/etc/systemd/system/strfry.service`:

```
[Unit]
Description=strfry nostr relay (relay-a)
After=network-online.target
Wants=network-online.target

[Service]
User=baron
Group=baron
ExecStart=/usr/local/bin/strfry --config=/etc/strfry.conf relay
Restart=on-failure
RestartSec=2
LimitNOFILE=1000000

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now strfry
sudo systemctl is-active strfry     # -> active
```

## Keeping the relays reconciled (Negentropy)

Run on **relay-b**, pulling from **relay-a** (runs as `baron`, who owns the LMDB
db — do NOT use sudo, which would create root-owned db files):

```bash
strfry --config=/etc/strfry.conf sync ws://relay-a.internal:7777 --dir=down
```

`--dir=both` reconciles in both directions. For continuous reconciliation, wrap
this in a systemd timer (future work; not required for ready-efe).

## Backfilling a relay that predates the history (ready-260)

Relay endpoints are configured **globally** (`~/.config/rd/rd.json`), so a newly
added relay is immediately a write target for every project. But an event only
reaches a relay as a **side effect of a write**. A project that has not been
written to since the relay was added has therefore never sent it anything —
nothing is misconfigured, the history simply predates the relay. To a browser
client reading only that relay, those boards do not exist, and there is no
in-product signal distinguishing "this board is empty" from "this board was
never published here".

The repair is a **verbatim re-send** of events already signed and already durable
in `.ready/nostr-log.jsonl`. Nothing is re-signed, rebuilt, or re-stamped: every
event keeps its original id, `created_at` and signature, so a relay that already
holds one treats it as a duplicate. That is what makes the whole operation
idempotent and safe to repeat. **Any path that re-signed would mint new event ids
and fork the history.**

```bash
# 1. See what WOULD be sent — no relay contact at all.
rd log publish --board --dry-run --relay wss://relay.3dl.network

# 2. Converge this project's board onto ONE relay: measure the gap, re-send only
#    that, repeat until the read-back matches.
rd relay repair --relay wss://relay.3dl.network --rounds 8

# 3. Independent proof (fresh REQ against that relay alone).
rd relay audit  --relay wss://relay.3dl.network
rd relay audit  --relay wss://relay.3dl.network --boards   # every 30301 it serves

# Whole portfolio, dry run then apply:
scripts/relay-backfill.sh --relay wss://relay.3dl.network
scripts/relay-backfill.sh --relay wss://relay.3dl.network --apply --jobs 2
```

**Never trust the publish report.** `reduceEventOutcome`
(`pkg/sync/relayclass.go`) reports an event as accepted when **any** write relay
accepts it. With the two LAN relays accepting everything, a total rejection by
the public relay is invisible in that report — the ready-f7b failure at bulk
scale. `--relay` pins a publish to one relay so its verdict is the whole verdict,
and `rd relay audit` re-reads that relay with fresh subscriptions.

**The audit compares projections, not counts.** Kinds 30301/30302/39301 are
addressable, so a relay retains one event per coordinate while the local log
retains every historical re-materialization. A correctly backfilled board
legitimately shows far fewer events on the relay than locally. What must match is
the set of addressable coordinates (and the winning event for each) plus every
non-addressable status event by id.

**Publish order matters.** A bulk re-send decides the relay's final projection
for addressable kinds, so events go out in ascending `created_at` with
same-second ties sent lowest-id **last** — the NIP-33 winner is the lowest id, so
this makes the winner the last write for its coordinate whether the relay
implements the tie-break or merely keeps the last write.

## NIP-65 relay discovery (outbox model)

The portfolio identity advertises its read/write relays via a NIP-65 `kind:10002`
relay-list event so other machines discover where to read/write without a
hardcoded hub:

```bash
nak event -k 10002 \
  -t "r=ws://relay-a.internal:7777;write" \
  -t "r=ws://relay-b.internal:7777;read" \
  ws://relay-a.internal:7777 ws://relay-b.internal:7777
```

(The demo uses a throwaway key; the real portfolio identity/key is handled by
ready-41d + the security review.)

## Verification (ground-source proof)

`scripts/relay-demo.sh` (in this repo) is the LIVE proof — no mocks. It:

1. Publishes a **signed** event to relay-a AND relay-b.
2. Reads it back by id from EACH relay.
3. Stops relay-a, proves read-back from relay-b; restores; stops relay-b, proves
   read-back from relay-a; restores.
4. Publishes + reads a NIP-65 `kind:10002` relay-list event.
5. Runs `strfry sync` to reconcile an event A→B via Negentropy.

Run it from the workshop VM (needs `nak` on PATH and SSH to the relay VMs):

```bash
go install github.com/fiatjaf/nak@latest   # once
scripts/relay-demo.sh
```

Captured output: `docs/relay-demo-output.txt`.

## Endpoint config for rd

Relay endpoints are surfaced to rd in `pkg/rdconfig` (`relay.go`):

- `rdconfig.DefaultRelays()` returns **none** — the ship default is
  **local-only** (no baked-in relay of any kind; `.ready/nostr-log.jsonl` alone
  is authoritative). This is NOT the three-relay topology described above —
  that set is this operator's own choice, recorded in `~/.config/rd/rd.json`,
  same as any other operator's would be. A fresh clone with no relay policy set
  anywhere in its ancestor chain talks to nothing until `rd init --relay <url>`,
  an interactive prompt at `rd init`, or a later edit to `rd.json` sets one —
  see "Updating relay_endpoints on an existing machine" above for that edit.
- `Config.RelayEndpoints` (`relay_endpoints` in `rd.json` or a project's
  `.ready/config.json`) — the actual configured set; resolution walks
  project config → committed board binding → home `rd.json` (`cmd/rd/nostr.go`).
- `Config.Relays()`, `Config.ReadRelayURLs()`, `Config.WriteRelayURLs()` —
  accessors downstream code (ready-a13) uses to discover where to read/write.

Wiring rd's actual read/write event mapping onto these relays is **out of scope
here** — that is downstream item **ready-a13**. This item only stands up the
relays, proves reachability/read-back/failover/sync, documents reproduction, and
surfaces the endpoint config.
