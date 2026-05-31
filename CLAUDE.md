# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

oxidize is a single Go binary that serves the [Oxide Console](https://github.com/oxidecomputer/console) SPA and implements the slice of the Oxide ("Nexus") external API the console calls, translating each request to the Proxmox VE API on the fly. One process serves both the UI and a relative `/v1/...` API on the same origin (no CORS). The console source is **not** vendored — it is cloned to `./console` (gitignored) and its build is embedded into the binary via `go:embed`.

```
browser ──▶ oxidize ──┬──▶ embedded Console SPA (internal/static/dist)
                      └──▶ /v1/* Oxide API ──translate──▶ Proxmox VE API
```

## Commands

```sh
make ui          # build console/dist (API_MODE=nexus => relative /v1, no mock SW) and copy into internal/static/dist
make build       # go build -o bin/oxidize ./cmd/oxidize  (needs internal/static/dist to exist; run `make ui` first)
make run         # build + run locally (needs PROXMOX_HOST + a TOKEN file)
make deploy      # cross-compile linux/amd64, scp the binary to the VM, restart the systemd service
make release     # make ui + deploy
make provision   # first-time install of the systemd unit on the VM (idempotent; never overwrites secrets)
make caddy       # install/refresh the Caddy reverse proxy (Tailscale HTTPS) on the VM

go test ./internal/...                                   # run all Go tests
go test ./internal/translate/ -run TestSanitizeName      # run a single test
```

Use `./internal/...` (not `./...`) for Go commands — `./...` walks the gitignored `console/node_modules`, which contains an unrelated stray Go package.

`make build` will fail without `internal/static/dist`; run `make ui` once first (it requires Node 20.19+/22+ and the `./console` checkout). When only changing Go code, you can rebuild with `go build ./cmd/oxidize` against an already-embedded dist.

## Architecture

The codebase is a layered translator. A request flows: **server** (route + auth + shape) → **proxmox** (typed PVE client) → **translate** (PVE struct → Oxide struct). Each layer is its own package under `internal/`:

- **`internal/oxide`** — hand-written Go structs for the Oxide API wire shapes (snake_case JSON). Also `Page()` (the `{items, next_page}` envelope), `WriteJSON`/`WriteError`, and `disk_state.go`. When the console expects a field, it's defined here.
- **`internal/proxmox`** — typed PVE client. `client.go` handles token auth, the `{"data": ...}` envelope, form-encoded writes, and async **task** polling (`tasks.go`; PVE mutations return a UPID you must poll). Files are grouped by PVE concept: `qemu.go`, `disks.go`, `storage.go`, `create.go`, `agent.go` (guest agent), `rrd.go` (metrics), `sdn.go`, `version.go`.
- **`internal/translate`** — pure PVE⇄Oxide mappers, no I/O. **`ids.go` is load-bearing**: Oxide uses UUIDs but Proxmox uses vmids/volids/node-names, and oxidize persists *no* id mapping — instead every id is a deterministic UUIDv5 of a stable string (`vm:<vmid>`, `disk:<vmid>:<dev>`, etc.) via `UUIDv5()`. To reverse a UUID back to a PVE resource, the handler lists PVE objects and re-derives each id to find the match. `SanitizeName()` coerces arbitrary strings into valid Oxide `Name`s.
- **`internal/server`** — all HTTP handlers and the single router. `server.go`'s `Handler()` is the **complete route table** — read it first to see what's mapped. Files are split per resource (`instances*.go`, `disks.go`, `network.go`, `floatingips.go`, etc.). `auth.go` implements the session (see below).
- **`internal/store`** — small file-backed JSON persistence (under `--data-dir`, default `data/`) for state with no Proxmox equivalent: SSH keys, floating IPs, IP pools, subnet pools, external subnets. Each store is mutex-guarded and tolerates a missing/corrupt file by returning empty.
- **`internal/static`** — `go:embed all:dist` + SPA fallback (real files served as-is, everything else → `index.html`) and the CSP/security headers mirroring `console/vercel.json`.
- **`internal/wsutil`** — WebSocket plumbing for the serial console (xterm-over-WS bridged to Proxmox's term protocol).

### Key conventions

- **Auth is a single synthetic user/silo/project.** `OXIDIZE_USER`/`OXIDIZE_PASS` gate the *UI*; the Proxmox token stays server-side and is the real credential. Login (`auth.go`) sets an HMAC-signed session cookie (`SessionSecret`); `s.protected(...)` wraps every authenticated route and returns a 401 Oxide error body, which the console turns into a client-side redirect to `/login`. There is no RBAC — `handleMe` reports `FleetViewer`/`SiloAdmin` true.
- **Synthetic singletons.** The project, silo, rack, default VPC/subnet, and system router are stable synthesized objects (ids in `translate/ids.go` and `translate/network.go`). Projects come from Proxmox resource pools; sleds map to PVE nodes; physical disks from node disk inventory.
- **Graceful 404s.** Unmapped `GET /v1/*` paths return an empty `Page` (so the console's list-prefetch loaders render an empty table instead of erroring); writes still 404. See `handleNotFound` and `emptyListRoutes` in `server.go`. When adding a console page that breaks on a missing endpoint, prefer wiring an empty-page stub over leaving it to the catch-all only if you need a typed shape.
- **PVE writes are async.** Mutations go through task polling bounded by `pveTimeout` (10s) / `cloneTimeout` (90s for template clones).

### External networking (SDN / floating IPs / external subnets)

This is the one place oxidize reaches beyond a pure read-translation and into the data plane of the homelab router host (`takahe`). Proxmox has no native allocatable-external-IP concept, so oxidize *synthesizes* it:

- Floating IPs, IP pools, subnet pools, and external subnets are oxidize-owned state in `internal/store`, surfaced through the matching `internal/server` handlers. They are allocated from `OXIDIZE_FLOATING_RANGE` / configured pools.
- The mapping from floating IP / external subnet → target instance IP is exposed at `GET /internal/floating-ip-map`, gated by `X-Oxidize-Token` (`OXIDIZE_INTERNAL_TOKEN`) rather than the browser session.
- `deploy/oxidize-fip-reconcile` (driven by `oxidize-fip.timer`/`.service` on takahe) polls that endpoint and reconciles it into the host's data plane: floating IPs → nftables DNAT rules in the `ip oxidize-fip` table; external subnets → `proto 200` routes. It leaves state untouched if oxidize is briefly unreachable.

When changing the internal map shape, update both `internal/server/floatingips.go` (producer) and `deploy/oxidize-fip-reconcile` (consumer) together.

## Gotchas

- The Proxmox API token (`./TOKEN`) and `*.env` files are gitignored secrets — never commit them.
- Many Oxide concepts cannot map cleanly to Proxmox and are deliberately read-only synthetic or stubbed (VPC firewall/routers, image upload, multi-user RBAC, per-disk snapshots). See the README "Limitations" before implementing one — confirm a real Proxmox mapping exists first.
- This server points at the user's **live Proxmox homelab**. Do not perform destructive actions (delete/wipe) on VMs or storage you did not create.
