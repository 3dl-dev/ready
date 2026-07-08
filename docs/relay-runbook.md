# Self-Hosted strfry Relay Runbook (ready-efe)

Reproducible-from-scratch runbook for the two-relay strfry (nostr) topology that
backs the rd nostr migration.

> **Invariant:** The relays are a **CACHE / always-available copy, NEVER the
> source of truth.** The source of truth is the campfire log. The relays exist so
> other machines can read/write events without a hardcoded hub, and so events
> survive a single relay going offline. Treat relay data as reconstructible at
> any time from the authoritative source.

## Topology

| Role    | VMID | Host                | LAN IP        | ws:// endpoint          |
|---------|------|---------------------|---------------|-------------------------|
| relay-a | 210  | mainframe (Proxmox) | 192.168.2.40  | `ws://192.168.2.40:7777` |
| relay-b | 211  | mainframe (Proxmox) | 192.168.2.41  | `ws://192.168.2.41:7777` |

Both VMs: Ubuntu 24.04 (Proxmox template 9000), 2 cores / 4 GB / 20 GB disk,
`--onboot 1` (survive host reboots). strfry built from source (`v1-b80cda3`),
run as a systemd service `strfry.service` under user `baron`, listening on
`0.0.0.0:7777`.

The relays live on the **mainframe Proxmox hypervisor**, not on any workshop /
migration VM, so they persist across the whole migration.

## Why two relays

Multi-relay topology gives availability: if either relay is offline, the other
still serves the full event set. They are kept reconciled with relay-to-relay
Negentropy (`strfry sync`, NIP-77). This is proven live by `scripts/relay-demo.sh`
(step 3 takes each relay offline in turn and reads back from the survivor;
step 5 reconciles an event A→B via Negentropy).

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
bash scripts/mk-relay.sh 210 relay-a 192.168.2.40
bash scripts/mk-relay.sh 211 relay-b 192.168.2.41
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
  "a9f766ae56bbf466d2d361e5b1788b7cd689fd8e3b418e35b002b313f478db25": "workshop VM portfolio key (machine-1)",
  "48ea98a915f44a28810c33c017c43dc7d5595f3541522c3bc8c90327ec9df497": "machine-2 rd-node portfolio key"
}
```

This is the SAME trust set as rd's **client-side** gate: `rdconfig.Config`
`TrustedPubkeys` + the self portfolio pubkey (`TrustSet`, ready-d53). Keep them
consistent — *an identity that may write to the relay is exactly an identity whose
events rd will ingest*. On each machine, `~/.cf/rd.json` `trusted_pubkeys` lists
the OTHER admitted machines (self is implicit); the relay allowlist lists them ALL
(there is no implicit self on a relay). The pubkeys are the `pubkey_hex` in each
machine's `~/.cf/nostr-identity.json` (materialized by rd,
`LoadOrCreatePortfolioKey`).

### Admitting a new pubkey

1. Add `"pubkey": "label"` to `scripts/relay-policy/write-allowlist.json`.
2. Add the pubkey to every *other* machine's `~/.cf/rd.json` `trusted_pubkeys`
   (client-side ingestion gate).
3. Re-run `scripts/lock-relays.sh` (idempotent) to push the updated allowlist to
   both relays. No strfry restart is needed for an allowlist-only change — the
   plugin reloads on mtime change — but `lock-relays.sh` restarts anyway to also
   reconcile config/plugin drift.

### Locking the relays from scratch

```bash
# From the workshop VM (needs ssh baron@<relay> + passwordless sudo on the relays):
scripts/lock-relays.sh                 # both relays (default)
scripts/lock-relays.sh 192.168.2.40    # a single relay
```

`mk-relay.sh` (mainframe repo) also installs the plugin + a seed allowlist at
provision time, so a freshly built relay is locked from first boot.

### Proof (ground-source, no mocks)

`scripts/relay-writepolicy-demo.sh` proves, on relay-a AND relay-b, that (a) the
allowlisted portfolio key publishes and is ACCEPTED, (b) a random untrusted key is
REJECTED with the relay's own block reason, and (c) reads stay open. Captured
output: `docs/relay-writepolicy-demo-output.txt`.

The Go live tests (`RD_NOSTR_LIVE_RELAY=1 go test ./pkg/sync/ ./pkg/nostr/`) sign
with the allowlisted key (`liveRelayKey`, resolved from
`~/.cf/nostr-identity.json` or `RD_NOSTR_TEST_SECRET_HEX`) and include
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
strfry --config=/etc/strfry.conf sync ws://192.168.2.40:7777 --dir=down
```

`--dir=both` reconciles in both directions. For continuous reconciliation, wrap
this in a systemd timer (future work; not required for ready-efe).

## NIP-65 relay discovery (outbox model)

The portfolio identity advertises its read/write relays via a NIP-65 `kind:10002`
relay-list event so other machines discover where to read/write without a
hardcoded hub:

```bash
nak event -k 10002 \
  -t "r=ws://192.168.2.40:7777;write" \
  -t "r=ws://192.168.2.41:7777;read" \
  ws://192.168.2.40:7777 ws://192.168.2.41:7777
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

- `rdconfig.DefaultRelays()` — the two relays above (both read+write).
- `Config.RelayEndpoints` (`relay_endpoints` in `rd.json`) — optional override.
- `Config.Relays()`, `Config.ReadRelayURLs()`, `Config.WriteRelayURLs()` —
  accessors downstream code (ready-a13) uses to discover where to read/write.

Wiring rd's actual read/write event mapping onto these relays is **out of scope
here** — that is downstream item **ready-a13**. This item only stands up the
relays, proves reachability/read-back/failover/sync, documents reproduction, and
surfaces the endpoint config.
