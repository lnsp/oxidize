# Plan: Real VPC firewall enforcement via Proxmox security groups

Status: proposal (not yet implemented)
Supersedes: the store-backed "recorded but not enforced" firewall rules
(`internal/store/firewallrules.go`), which becomes the **desired-state source of
truth** for this design rather than being thrown away.

## Goal

Make `PUT /v1/vpc-firewall-rules` actually filter traffic, by translating an
Oxide VPC's rule set into a Proxmox **cluster security group** + **IPsets** and
attaching that group to every VM in the VPC. The console firewall page keeps
working exactly as it does today; the difference is the rules now take effect on
the data plane.

### Non-goals
- Perfect semantic parity with Oxide's firewall evaluation model (see
  *Fidelity gaps*). We aim for faithful, predictable, and safe — not bit-exact.
- Enforcing on the flat-LAN default VPC by default (see *Safety* — opt-in only).
- Replacing or touching firewall rules a user created by hand.

## Why security groups (not per-VM rule replication)

A Proxmox **security group** (`/cluster/firewall/groups/<name>`) is a named,
reusable list of in/out rules. A VM references it with a single rule of type
`group`. This matches Oxide's VPC-scoped model almost exactly:

- **One group per VPC**, defined once, holding *all* the VPC's rules.
- Each member VM gets **one** `group` reference rule — not a replicated copy of
  every rule. Adding a VM to the VPC = add one rule. Teardown is trivial.
- A rule's `targets` and `filters.hosts` both become `source`/`dest`
  constraints **inside the group rule** (see mapping), so a rule that targets
  only instance `web` is simply inert on other members (their traffic never
  matches `dest`) — no per-VM specialization needed.

This is dramatically fewer API writes and far cleaner ownership than pushing
N copies of every rule onto N VMs.

## The mapping

For each Oxide rule, direction decides which side is the target vs the host
filter:

| Oxide rule | PVE group rule |
|---|---|
| `action: allow` / `deny` | `action: ACCEPT` / `DROP` |
| `direction: inbound` | `type: in`, `source` = `filters.hosts`, `dest` = `targets` |
| `direction: outbound` | `type: out`, `source` = `targets`, `dest` = `filters.hosts` |
| `filters.ports` | `dport` (PVE accepts `80`, `8000:8100`, comma lists) |
| `filters.protocols: [tcp]` | `proto: tcp` (one PVE rule per protocol — fan out) |
| `status: disabled` | `enable: 0` |
| `priority` | rule `pos` (ascending priority → ascending position) |
| `description` | `comment` (prefixed `oxidize:<vpcID>` for ownership) |

### Resolving `targets` and `filters.hosts` to addresses
Both are `{type, value}` where type ∈ {vpc, subnet, instance, ip, ip_net}:

- `ip` / `ip_net` → literal `source`/`dest` (`1.2.3.4` / `1.2.3.0/24`).
- `vpc` → an **IPset** of all member IPs of that VPC, referenced as `+<ipset>`.
- `subnet` → the subnet CIDR (cheap) or an IPset of its member IPs.
- `instance` → the instance's private IP(s), resolved via the same machinery
  the floating-IP map uses (`newIPResolveCtx` / `instancePrivateIPWith` in
  `instances.go` + SDN IPAM). Multiple IPs → IPset.

IPsets are created at cluster scope (`/cluster/firewall/ipset/<name>`) and kept
in sync by the reconciler. Names are deterministic and length-bounded (PVE caps
group/ipset names; we hash-suffix — see *Naming*).

### Protocols / ports fan-out
A single Oxide rule with `protocols: [tcp, udp]` and `ports: [80, 443]` expands
to PVE rules per protocol (PVE rule = one `proto`), each with `dport: 80,443`.
Empty protocols + empty ports → one any-protocol rule.

## Proxmox API surface

All synchronous (no UPID/task polling). New typed client in
`internal/proxmox/firewall.go`:

- Security groups: `GET/POST/DELETE /cluster/firewall/groups[/<group>]`
- Group rules: `GET/POST/DELETE /cluster/firewall/groups/<group>/rules[/<pos>]`
- Cluster IPsets: `GET/POST/DELETE /cluster/firewall/ipset[/<name>]` and
  `.../ipset/<name>/<cidr>` for members
- Per-VM firewall options: `GET/PUT /nodes/<node>/qemu/<vmid>/firewall/options`
  (we set `enable: 1`, `policy_in: ACCEPT` — see *Safety*)
- Per-VM rules: `GET/POST/DELETE /nodes/<node>/qemu/<vmid>/firewall/rules` (just
  the one `group` reference rule, tagged `oxidize:<vpcID>`)
- NIC firewall flag: set `firewall=1` on the member `netN` device via the
  existing `qemu.UpdateConfig` (no new endpoint)

## Safety (the part that matters on a live cluster)

1. **Lockout avoidance.** Enabling the VM firewall defaults `policy_in` to DROP
   in Proxmox; combined with a rule set that forgets SSH/console, that bricks
   access. We therefore set **`policy_in: ACCEPT`** and express Oxide's intent as
   explicit ACCEPT/DROP rules. Faithful "default-deny inbound" is available only
   behind an explicit opt-in (config `FirewallDefaultDeny`, off by default) and
   even then only after a guard rule preserving the cluster's management network.
   → **Open decision #1.**
2. **Default VPC (flat LAN) excluded by default.** Its members are *every* VM on
   `vmbr0`, including ones the user manages by hand. Enforcing there is the most
   dangerous. By default we enforce **only on SDN-backed VPCs** (networks oxidize
   itself created). → **Open decision #2.**
3. **Strict ownership.** Every object oxidize writes is tagged:
   group named `oxidize-<hash>`, rules/ipsets commented `oxidize:<vpcID>`. The
   reconciler **only ever creates or deletes objects carrying that tag** — a
   user's hand-written PVE rules are never read, moved, or removed.
4. **Conservative teardown.** On VPC rule-set clear / VPC delete, we remove our
   group reference rule, group, and ipsets, but **leave** `firewall=1` on NICs
   and `enable` in VM options as-is (additive, never disabling what the user may
   want). Documented.
5. **Three-state rollout flag** (config `FirewallMode`):
   - `off` (default) — today's behavior exactly: store + round-trip, no apply.
   - `dryrun` — compute the full desired PVE state and `log()` every create/
     delete it *would* perform, apply nothing.
   - `on` — apply.

## Architecture: desired state + reconciler

The `FirewallRuleStore` stays as the **desired state**. A reconciler turns
desired state into live PVE objects, idempotently, and is the *only* writer.

```
PUT /v1/vpc-firewall-rules ──▶ store.Replace(vpcID, rules)
                              └▶ reconcileVPC(vpcID)        (immediate)

periodic goroutine (every FirewallReconcileInterval, default 30s)
        └▶ for each enforced VPC: reconcileVPC(vpcID)       (drift: VMs joined/
                                                             left, IP changes)
```

`reconcileVPC(vpcID)` (pure-ish; the apply step is the only I/O):

```
1. members = VMs with ≥1 NIC whose bridge maps to vpcID   (netLocator over QemuConfig)
   memberNICs = the specific netN devices on that VPC      (for firewall=1)
2. desired = translate.FirewallPlan(storeRules[vpcID], resolver)
      → { group: []PVEGroupRule, ipsets: map[name][]cidr }
   resolver resolves vpc/subnet/instance refs → IPs using the FIP IP machinery
3. live = read owned (oxidize-tagged) group + ipsets
4. diff desired vs live; in `on` mode apply the delta (under fwMu, so the loop
   and a PUT-triggered apply of the same VPC can't race):
      - upsert ipsets + members
      - replace the group's oxidize rules (delete tagged, recreate in order),
        UNLESS the rule set's content hash == the last-applied hash and the live
        rule count is unchanged (churn guard: compares our own hash, not PVE's
        echoed fields, while the count check still heals gross drift)
      - for each member VM: ensure firewall enabled, NICs firewall=1, one
        `group` reference rule present (tagged)
      - for VMs no longer members: remove the tagged group reference rule
   in `dryrun` mode: log the delta, apply nothing
5. orphan sweep: groups/ipsets tagged for a vpcID that no longer has stored
   rules → delete (in `on`), or log (in `dryrun`)
```

The translate step (`internal/translate/firewall.go`) is **pure** and the bulk
of the correctness surface, so it gets heavy unit tests with zero cluster risk.

## Naming

PVE caps group/ipset names (~18 chars, `[A-Za-z0-9-]`). VPC ids are UUIDs, so:
- group: `oxidize-<first 10 hex of sha1(vpcID)>`
- ipset: `ox-<kind>-<first 8 hex of sha1(vpcID:ref)>` (kind = v/s/i for
  vpc/subnet/instance)

Deterministic, collision-resistant, reversible-via-scan (we list and match by
the `oxidize:<vpcID>` comment, not by parsing the name).

## File-by-file

| File | Change |
|---|---|
| `internal/proxmox/firewall.go` | **new** — typed client: security groups, group rules, cluster ipsets, per-VM firewall options/rules. Synchronous. |
| `internal/translate/firewall.go` | **new** — pure `FirewallPlan(rules, resolver) → {groupRules, ipsets}`, protocol/port fan-out, direction→source/dest, deterministic names. No I/O. |
| `internal/translate/firewall_test.go` | **new** — table tests: each direction, multi-protocol fan-out, each host/target type, disabled rule, ordering, name stability. |
| `internal/server/firewall_reconcile.go` | **new** — `reconcileVPC`, member/NIC resolution (reuses `netLocator`, `QemuConfig`, FIP IP resolver), live-state read, diff, apply/dry-run, orphan sweep, and the periodic loop + `Start`/stop wiring. |
| `internal/server/network.go` | `handleFirewallRulesUpdate` calls `reconcileVPC(vpcID)` after `store.Replace` (skip in `off`); `vpcIDForRef` already exists. |
| `internal/server/server.go` | hold the firewall client + reconciler; start the goroutine when `FirewallMode != off`; stop on shutdown. |
| `internal/config/*.go` | add `FirewallMode` (off/dryrun/on), `FirewallDefaultDeny` bool, `FirewallReconcileInterval`. |
| `cmd/oxidize/main.go` | flags + env (`OXIDIZE_FIREWALL_MODE`, etc.); thread into `config.Config`. |
| `internal/store/firewallrules.go` | doc comment updated: now desired state for the reconciler (no API change). |
| `README.md` | move firewall rules out of "synthetic/limitations" into a documented, opt-in enforced feature with its caveats. |

`server.New` gains the firewall proxmox client; no new store. The reconciler
reuses existing IP-resolution helpers rather than duplicating them.

## Fidelity gaps (documented, not hidden)

- **Rule ordering / precedence.** Oxide evaluates by priority with allow/deny;
  PVE is first-match by position. We sort by ascending priority and rely on
  explicit allow/deny — edge cases with equal priorities + conflicting actions
  may differ. Documented.
- **Default-deny inbound.** Off by default for lockout safety (see Safety #1);
  with `FirewallDefaultDeny=off`, rules are additive allow/deny over an ACCEPT
  baseline, which is *more permissive* than Oxide's implicit deny-all.
- **Stateful nuances.** PVE conntrack is on by default; Oxide's
  established/related semantics are approximated, not modeled rule-by-rule.
- **Flat-LAN VPC.** Record-only unless explicitly opted in.

## Testing & rollout

1. **Unit:** `translate/firewall.go` is pure → exhaustive table tests (the real
   correctness surface). Reconciler diff logic tested against a small faked
   firewall-client interface (introduce a narrow interface so the reconciler
   doesn't need a live `*proxmox.Client`).
2. **Dry-run on the live cluster:** ship `FirewallMode=dryrun`, create a rule in
   a throwaway SDN VPC, inspect the logged plan — zero writes.
3. **One VPC, one disposable VM:** flip to `on` for a test VPC with a VM you
   own; verify rules appear in the PVE UI and traffic is filtered; verify you
   are not locked out (policy_in=ACCEPT).
4. **Enable broadly** once validated.

## Decisions (settled 2026-05-31)

1. **Default-deny inbound:** OFF. `policy_in: ACCEPT` baseline; rules are
   additive allow/deny. More permissive than Oxide, but no lockout risk. True
   default-deny stays a future opt-in.
2. **Flat-LAN default VPC:** record-only. Enforcement applies **only to
   SDN-backed VPCs** oxidize created.
3. **Reconcile transport:** in-process goroutine (oxidize holds the PVE client
   and already does live SDN writes).
4. **Reconcile interval:** 30s default (revisit later); NIC create/delete
   triggering an immediate reconcile is a possible later optimization.

## Build order

1. **`internal/translate/firewall.go` + tests first** (this step) — the pure
   `FirewallPlan(vpcID, rules, resolver)` mapping, reviewable with zero cluster
   risk. Everything below depends on it.
2. `internal/proxmox/firewall.go` — typed PVE client.
3. `internal/server/firewall_reconcile.go` — member resolution, live-state
   diff, apply/dry-run, periodic loop.
4. config/flags + `network.go` trigger + README.
