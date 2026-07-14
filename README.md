# rd — work management as a nostr convention

`rd` surfaces what needs attention. No separate backend — a local append-only
signed-event log (`.ready/nostr-log.jsonl`) is the source of truth, synced over
nostr relays as a replaceable cache. Primary users are AI agents; humans use it
too.

## Install

```bash
curl -fsSL https://ready.3dl.dev/install.sh | sh
# or: go install github.com/3dl-dev/ready/cmd/rd@latest
```

Self-contained binary, no separate identity ceremony — a secp256k1 signing key
is created on first use under `$RD_HOME` (default `~/.config/rd`).

---

## For agents

### Drop into CLAUDE.md

```markdown
## Work Management
rd is available on PATH. It auto-detects the project from your working directory.
Run `rd ready` at session start. Claim before working. Close with a reason.
After context loss: `rd ready --view work` to see what you were doing, `rd show <id>` to reload context.
For recurring work shapes (incident runbooks, release prep, feature rollouts), check `rd playbook list` before decomposing by hand — `rd engage <id>` stamps a template into real items with deps wired.
```

### Install the skill

```bash
curl -fsSL https://ready.3dl.dev/claude-skill.sh | sh
```

### The work loop

```bash
rd ready                                     # what's actionable right now?
rd update <id> --status in_progress          # claim it
# ... do the work ...
rd done <id> --reason "fixed: was checking issuer not audience"
rd ready                                     # what unlocked?
```

### Pipe-friendly patterns

When stdout is a pipe, `rd create`, `rd ready`, and `rd list` print bare IDs — no decoration.

```bash
# Capture a new item ID
ITEM=$(rd create "Auth returns 403 on valid tokens" --type task --priority p0)

# Work every ready item in a loop
for id in $(rd ready); do
  rd update "$id" --status in_progress
  # ... work it ...
  rd done "$id" --reason "done"
done

# JSON for richer queries
rd list --json | python3 -c "import sys,json; [print(i['id']) for i in json.load(sys.stdin) if i['priority']=='p0']"
```

### After context loss

```bash
rd ready --view work    # items you had in_progress
rd show <id>            # full description + audit trail
```

---

## For teams

### Invite / join — mint-and-ship, no separate key exchange

```bash
# One person creates the project and issues a token:
rd invite                     # mints a fresh secp256k1 identity, publishes an
                               # owner-signed grant for it, and bundles the board
                               # coordinate + relays + TTL + secret into an rd1_ token

# Teammate joins with one command:
rd join rd1_...                # imports the minted identity, pins the board,
                                # adopts the relays, and syncs — ready to go
```

Tokens are single-use: the first redeemer publishes a signed consumed marker.
Use `--ttl` on `rd invite` to limit the exposure window (default 2h).

### Identity — `$RD_HOME`, no key exchange at runtime

Every identity is a secp256k1 signing key under `$RD_HOME` (default
`~/.config/rd`, overridable with `--rd-home` or `$RD_HOME`). There is no
separate identity file to walk up or symlink — the key is resolved once per
process from `$RD_HOME`.

Agents running in isolated worktrees get isolated identities by pointing
`$RD_HOME` at a worktree-local directory:

```bash
RD_HOME=worktrees/feature-x/.rd rd join rd1_<token-for-agent>
```

### Authorization — kind-39301 owner-signed grants

Delegation is a signed act, not implicit trust. The board owner publishes an
owner-signed `kind-39301` role-grant naming a grantee pubkey and a role; the
relay write-allowlist and the client trust set are both derived from the same
signed log, so there is one source of truth for "who can write here."

```bash
rd grant <pubkeyHex> contributor   # grant a role
rd sessions                              # list active grant-holders
rd kill <pubkeyHex>                      # revoke a grant-holder's delegation
```

### Gate escalation — agent blocks on human decision

```bash
# Agent encounters a decision it can't make:
rd gate <item-id> --question "Use pessimistic or optimistic locking?" --context "Observed concurrent requests in prod"

# Human sees the gate in rd ready, approves:
rd approve <gate-id> --ruling "Pessimistic. Reason: concurrent request rate too high for optimistic."

# Agent resumes — rd show <item-id> now includes the ruling
```

---

## Quick reference

| Command | What it does |
|---------|-------------|
| `rd init [--name <project>]` | Initialize a nostr-native project (creates `.ready/`, signing key under `$RD_HOME` on first use) |
| `rd invite` | Mint a one-use `rd1_` invite token (fresh identity + owner-signed grant, bundled) |
| `rd join rd1_...` | Join a project via an invite token |
| `rd create "..." [--type task] [--priority p0]` | Create a work item |
| `rd ready` | What's actionable now (auto-synced) |
| `rd ready --view work` | Items currently in_progress |
| `rd list` | All open items |
| `rd show <id>` | Item details + audit trail |
| `rd update <id> --status in_progress` | Claim an item |
| `rd done <id> --reason "..."` | Close with reason |
| `rd update <id> --note "..."` | Add a progress note |
| `rd dep add <child> <blocker>` | Wire a dependency (cross-project OK) |
| `rd dep tree <id>` | View dependency hierarchy |
| `rd gate <id> --question "..."` | Block item on human decision |
| `rd approve <gate-id> --ruling "..."` | Fulfill a gate |
| `rd grant <pubkeyHex> <role>` | Publish an owner-signed role grant |
| `rd sessions` | List active grant-holders |
| `rd kill <pubkeyHex>` | Revoke a grant-holder's delegation |
| `rd migrate` | Re-emit a legacy campfire item set as nostr events, preserving ids + history |
| `rd migrate --parity` | Assert item-for-item parity between legacy source and nostr projection |
| `rd playbook list` | List registered playbook templates |
| `rd playbook create "..." --id <id> --items-file <path>` | Register a reusable work tree (store-free, `.ready/playbooks.jsonl`) |
| `rd playbook show <id>` | Inspect a playbook's item tree |
| `rd engage <id> --project <p> --for <who> --var k=v` | Stamp a playbook into work items |

**Item fields:** `type` (task, decision, review, reminder, deadline), `priority` (p0–p3), `status` (inbox, in_progress, waiting, blocked, done, cancelled, failed), `due`, `eta`

---

## How it works

Ready is a **convention**, not an application. Work items are structured
operations (`work:create`, `work:claim`, `work:close`, etc.) projected as
signed nostr events — a `kind-30301` board, `kind-30302` cards, and a status
log per item. `rd` is a thin CLI wrapper that speaks this convention over a
local authoritative log, with relays as a replaceable sync cache.

`rd list`, `rd ready`, and `rd show` read from the local log on every call —
no manual sync step.

**WHO is first-class.** Every item has `for` (who needs the outcome) and `by` (who's doing the work). Delegation is an explicit act.

**Attention engine.** `rd ready` filters to what's actionable for your identity right now. An agent's view shows what's assigned to it.

---

## Migrating an existing campfire project

If your project still runs on the retired campfire backend (`.campfire/`),
migrate it to the nostr-native log without losing history:

```bash
rd migrate            # re-emits the legacy item set as nostr events —
                       # ids and full status history are preserved; the
                       # legacy source is left intact (non-destructive)

rd migrate --parity   # verifies item-for-item field equality between the
                       # legacy source and the nostr projection; exits
                       # non-zero on any mismatch — run this before trusting
                       # the migration

rm -rf .campfire      # once parity passes, drop the legacy store
```

`rd migrate` is idempotent by event id, so it is safe to re-run. See
`docs/nostr-migration.md` for the full migration + dual-read design.

---

- [docs/getting-started.md](docs/getting-started.md) — full walkthrough
- [docs/relay-runbook.md](docs/relay-runbook.md) — operating a relay

MIT License
