#!/usr/bin/env python3
"""strfry writePolicy plugin — rd trusted-portfolio-pubkey write-allowlist (ready-266).

Locks the mainframe strfry relays (relay-a @ 192.168.2.40, relay-b @ 192.168.2.41)
so that only ADMITTED portfolio identities may WRITE (publish events). Reads stay
open — strfry's writePolicy only governs the write path (incoming EVENTs); REQ/read
subscriptions are unaffected, so the relays remain a public read cache.

WHY AUTHOR-PUBKEY ENFORCEMENT (no NIP-42 AUTH):
  strfry validates every event's id + BIP-340 schnorr signature BEFORE invoking
  this plugin. So when this plugin sees event.pubkey == P, strfry has already
  proven the event was signed by the holder of P's secret key. An attacker cannot
  forge an allowlisted author. Therefore checking event.pubkey against the
  allowlist IS enforcement of "only admitted keys may write" — a NIP-42 AUTH
  challenge would add nothing for signed-event write control. This is
  defence-in-depth ON TOP of rd's client-side web-of-trust ingestion gate
  (ready-d53); neither layer replaces the other.

PROTOCOL (strfry writePolicy plugin, https://github.com/hoytech/strfry):
  strfry launches this script once and keeps it running, feeding one JSON object
  per line on stdin and reading one JSON decision per line on stdout:
    in  : {"type":"new","event":{...,"pubkey":"<hex>"},"sourceType":...,...}
    out : {"id":"<event id>","action":"accept"|"reject","msg":"<reason>"}
  Only messages with type=="new" carry an event to decide on; other control
  messages ("lookback", etc.) are acknowledged as accept with no id.

ALLOWLIST SOURCE OF TRUTH:
  A JSON file (default /etc/strfry/write-allowlist.json), a single object mapping
  each admitted lowercase-hex x-only pubkey to a human label:
    { "a9f7...": "workshop VM portfolio key",
      "48ea...": "machine-2 (rd-node) portfolio key" }
  This mirrors rd's client-side trust set: rdconfig.Config.TrustedPubkeys plus the
  self portfolio pubkey (see pkg/rdconfig/config.go TrustSet, ready-d53). Keep the
  two consistent — an identity that may write to the relay is exactly an identity
  whose events rd will ingest. The file is re-read whenever its mtime changes, so
  admitting a new pubkey takes effect WITHOUT restarting strfry.

FAIL-CLOSED: if the allowlist file is missing, unreadable, malformed, or empty,
  the plugin REJECTS every write (a security allowlist must fail closed — an
  absent list means "trust nobody", never "trust everybody"). Diagnostics go to
  stderr, which strfry captures in its logs.
"""

import json
import os
import sys
import time

ALLOWLIST_PATH = os.environ.get("STRFRY_WRITE_ALLOWLIST", "/etc/strfry/write-allowlist.json")

_cache = {"mtime": None, "pubkeys": frozenset(), "error": "not yet loaded"}


def load_allowlist():
    """Return a frozenset of admitted lowercase-hex pubkeys, reloading only when
    the file's mtime changes. On any error the set is empty and _cache['error']
    is set so decide() can fail closed with a diagnostic."""
    try:
        st = os.stat(ALLOWLIST_PATH)
    except OSError as e:
        if _cache["error"] != str(e):
            print(f"rd-write-allowlist: cannot stat {ALLOWLIST_PATH}: {e} "
                  f"(failing CLOSED — rejecting all writes)", file=sys.stderr, flush=True)
        _cache["mtime"] = None
        _cache["pubkeys"] = frozenset()
        _cache["error"] = str(e)
        return _cache["pubkeys"]

    if _cache["mtime"] == st.st_mtime and _cache["error"] is None:
        return _cache["pubkeys"]

    try:
        with open(ALLOWLIST_PATH, "r", encoding="utf-8") as f:
            data = json.load(f)
        if not isinstance(data, dict):
            raise ValueError("allowlist must be a JSON object {pubkey: label}")
        pubkeys = frozenset(
            k.strip().lower() for k in data.keys() if isinstance(k, str) and k.strip()
        )
        if not pubkeys:
            raise ValueError("allowlist is empty (trust nobody)")
        _cache["mtime"] = st.st_mtime
        _cache["pubkeys"] = pubkeys
        _cache["error"] = None
        print(f"rd-write-allowlist: loaded {len(pubkeys)} admitted pubkey(s) from "
              f"{ALLOWLIST_PATH}", file=sys.stderr, flush=True)
        return pubkeys
    except (OSError, ValueError, json.JSONDecodeError) as e:
        print(f"rd-write-allowlist: bad allowlist {ALLOWLIST_PATH}: {e} "
              f"(failing CLOSED — rejecting all writes)", file=sys.stderr, flush=True)
        _cache["mtime"] = st.st_mtime
        _cache["pubkeys"] = frozenset()
        _cache["error"] = str(e)
        return frozenset()


def decide(msg):
    """Map one strfry writePolicy request to a decision dict."""
    if msg.get("type") != "new":
        # Non-event control message: nothing to authorize.
        return {"id": "", "action": "accept"}

    event = msg.get("event") or {}
    eid = event.get("id", "")
    pubkey = (event.get("pubkey") or "").strip().lower()

    allow = load_allowlist()
    if pubkey and pubkey in allow:
        return {"id": eid, "action": "accept"}
    return {
        "id": eid,
        "action": "reject",
        "msg": "blocked: pubkey not in rd trusted-portfolio write-allowlist (ready-266)",
    }


def main():
    # Prime the cache (and emit a startup diagnostic) before the first event.
    load_allowlist()
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        try:
            msg = json.loads(line)
        except json.JSONDecodeError as e:
            # Cannot parse the request; fail closed but keep the pipe alive.
            print(f"rd-write-allowlist: unparseable request: {e}", file=sys.stderr, flush=True)
            sys.stdout.write(json.dumps({"id": "", "action": "reject",
                                         "msg": "malformed writePolicy request"}) + "\n")
            sys.stdout.flush()
            continue
        out = decide(msg)
        sys.stdout.write(json.dumps(out) + "\n")
        sys.stdout.flush()


if __name__ == "__main__":
    main()
