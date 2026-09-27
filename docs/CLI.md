# NIBIA Fabric CLI reference

This document describes the public command-line surface shipped with **NIBIA Fabric v0.7.0-alpha**.

The CLI is experimental and may change between alpha releases. For the shortest path from a release download to a working Fabric, start with [QUICKSTART.md](../QUICKSTART.md).

Only the commands documented here are part of this release's public CLI surface. The primary inference paths are `nibia run` for terminal/one-shot use and `nibia serve` for a persistent local Web UI/API. Worker/relay lifecycle commands remain available for advanced diagnostics and explicit control.

## Binaries

NIBIA Fabric ships three command-line programs:

| Binary | Purpose |
| --- | --- |
| `nibia` | User CLI for setup, diagnostics, Fabric inspection, managed runtime control, and GGUF inference/serving |
| `nibia-controller` | Primary-node Controller and authenticated reverse-relay listener |
| `nibia-agent` | Persistent node Agent used on the Primary and Worker Nodes |

Run `nibia`, `nibia --help`, or `nibia help` to print the concise user-facing command map. Run `nibia help advanced` for maintenance and low-level Fabric controls. Command and namespace help consistently accept both `-h` and `--help`.

Unless a command says otherwise, Controller-backed CLI commands use `http://127.0.0.1:8080` by default.

## Command map

Primary user-facing commands:

```text
nibia serve
nibia run
nibia nodes
nibia doctor
nibia discover
nibia pair
nibia trust list
nibia trust revoke <node>
nibia node inspect <node>
nibia version
```

Advanced and maintenance commands:

```text
nibia setup
nibia runtime status
nibia runtime ensure
nibia agent reset
nibia controller info
nibia fabric status
nibia fabric devices
nibia fabric worker start|status|stop
nibia fabric relay start|status|probe|stop
```

## Version and local setup

### `nibia version`

Prints the NIBIA version, wire protocol, and current platform.

### `nibia setup`

```text
nibia setup [--timeout <duration>]
```

| Flag | Default | Purpose |
| --- | --- | --- |
| `--timeout` | `20m` | Maximum managed-runtime bootstrap time |

Ensures the pinned NIBIA-managed llama.cpp runtime is installed and verified.

### `nibia doctor`

```text
nibia doctor [--local] [--controller <url>]
```

| Flag | Default | Purpose |
| --- | --- | --- |
| `--local` | `false` | Check only the current machine and skip Controller/Fabric checks |
| `--controller` | `http://127.0.0.1:8080` | Local Controller URL |

## Discovery, pairing, and trust

### `nibia discover`

```text
nibia discover [options]
```

| Flag | Default | Purpose |
| --- | --- | --- |
| `--timeout` | `3s` | Discovery timeout |
| `--include-virtual` | `false` | Include VPN/tunnel/virtual interfaces |
| `--no-local` | `false` | Disable localhost Controller fallback |
| `--local-port` | `8080` | Local pairing/info port used by fallback |
| `--verbose` | `false` | Show interfaces used for discovery |

### `nibia pair`

```text
nibia pair [--controller <url>]
```

Creates a short-lived pairing code through the Controller pairing endpoint.

### `nibia trust list`

```text
nibia trust list [--controller <url>]
```

Lists trusted Agent identities known by the Controller.

### `nibia trust revoke`

```text
nibia trust revoke <node-name-or-id> [--controller <url>]
```

Revokes the selected Agent trust record.

### `nibia agent reset`

```text
nibia agent reset --state-dir <path> --yes
```

| Flag | Default | Purpose |
| --- | --- | --- |
| `--state-dir` | platform user Agent state directory | Agent identity/state directory to reset |
| `--yes` | `false` | Required confirmation for the destructive reset |

After a reset, the Agent must be paired again.

## Fabric and node inspection

### `nibia nodes`

```text
nibia nodes [--controller <url>]
```

Shows current node state, runtime parity, CPU topology, available RAM, SAFE usable capacity, resource pressure, and Agent freshness.

### `nibia node inspect`

```text
nibia node inspect <name-or-id> [--controller <url>]
```

Prints detailed telemetry, network interfaces, capabilities, runtime identity, CPU/RAM state, and last-seen information for one node.

### `nibia controller info`

```text
nibia controller info [--controller <url>]
```

Shows Controller version, protocol compatibility, secure URL, and fingerprint.

## Managed llama.cpp runtime

### `nibia runtime status`

```text
nibia runtime status
```

Reports the local managed llama.cpp runtime state.

### `nibia runtime ensure`

```text
nibia runtime ensure [--timeout <duration>]
```

| Flag | Default | Purpose |
| --- | --- | --- |
| `--timeout` | `20m` | Maximum runtime download/install time |

Downloads or verifies the release-pinned managed runtime.

## Inference commands

## `run` vs `serve`

- Use **`nibia run`** for one prompt, scripting, benchmarks, or terminal inference that should exit after the response.
- Use **`nibia serve`** when the model should stay loaded for the built-in Web UI, the local OpenAI-compatible API, or repeated requests. Stop SERVE with `Ctrl+C`; NIBIA then cleans up the workload resources.
- Internally, the managed llama.cpp runtime uses `llama-cli` for RUN and `llama-server` for SERVE. End users normally invoke NIBIA rather than those runtime binaries directly.

### `nibia run`

```text
nibia run --model <file.gguf> [options]
```

Runs llama.cpp inference using safe local/distributed placement. One-shot RUN requires `--prompt`, executes that prompt, prints the response, cleans up the Fabric workload, and exits. Use `--session` when you intentionally want an interactive terminal session; in session mode the initial `--prompt` is optional.

| Flag | Default | Purpose |
| --- | --- | --- |
| `--model` | required | Local GGUF path on the Primary Node |
| `--prompt` | required for one-shot; empty with `--session` | Initial prompt |
| `--tokens` | `64` | Maximum tokens per response |
| `--ctx` | `4096` | Context size |
| `--session` | `false` | Keep `llama-cli` interactive after the first response |
| `--nodes` | empty | Comma-separated candidate Workers; empty means auto-discover |
| `--node` | empty | Single-worker alias |
| `--force-distributed` | `false` | Use remote devices even when the model fits locally |
| `--auto-start` | `true` | Start managed workers/relays when needed; auto-started resources are cleaned up on RUN/session exit. With `false`, caller-managed workers/relays are preserved. |
| `--node-grace` | `15s` | Grace window for transient Worker recovery |
| `--planning-timeout` | `20s` | Timeout for each device-discovery attempt |
| `--memory-reserve` | empty | Per-node reserve overrides, for example `primary=2GiB,worker-a=20%` |
| `--reserve-mb` | none | Legacy global fixed reserve override in MiB; adaptive reservation is used when this flag is omitted |
| `--load-mode` | `none` | llama.cpp load mode: `none` or `auto` |
| `--progress` | `true` | Show distributed loading progress |
| `--verbose` | `false` | Show detailed NIBIA planning, RPC contribution, and process telemetry |
| `--timeout` | `20m` | Maximum runtime; `0` disables the timeout |
| `--verbose-runtime` | `false` | Show raw llama.cpp startup/runtime output |
| `--allow-runtime-mismatch` | `false` | Development escape hatch for incompatible llama.cpp source identities |
| `--controller` | `http://127.0.0.1:8080` | Local Controller URL |

The default reserve policy is adaptive: 10% of physical RAM per node, with a 512 MiB minimum and a 4 GiB maximum, unless an explicit reserve override is supplied.

### `nibia serve`

```text
nibia serve --model <file.gguf> [options]
```

Starts the persistent local llama.cpp server and keeps the model resident until `Ctrl+C`. Use this when you want the built-in Web UI, the OpenAI-compatible API, or multiple requests without reloading the model.

| Flag | Default | Purpose |
| --- | --- | --- |
| `--model` | required | Local GGUF path on the Primary Node |
| `--ctx` | `4096` | Server context size |
| `--host` | `127.0.0.1` | llama-server bind host; loopback-only in this Experimental Alpha |
| `--port` | `8081` | llama-server port |
| `--open-ui` | `loading` | Open Web UI at `loading`, `ready`, or `never` |
| `--nodes` | empty | Comma-separated candidate Workers; empty means auto-discover |
| `--node` | empty | Single-worker alias |
| `--force-distributed` | `false` | Use remote devices even when the model fits locally |
| `--auto-start` | `true` | Start managed workers/relays when needed; auto-started resources are cleaned up on SERVE exit. With `false`, caller-managed workers/relays are preserved. |
| `--auto-recover` | `true` | Keep SERVE alive and rebuild after a selected Worker reconnects |
| `--recovery-timeout` | `5m` | Maximum wait for selected Worker capacity to recover |
| `--node-grace` | `15s` | Grace for recently stale/OFFLINE Worker capacity when needed |
| `--planning-timeout` | `20s` | Timeout for each device-discovery attempt |
| `--memory-reserve` | empty | Per-node reserve overrides, for example `primary=2GiB,worker-a=20%` |
| `--reserve-mb` | none | Legacy global fixed reserve override in MiB; adaptive reservation is used when this flag is omitted |
| `--load-mode` | `none` | llama.cpp load mode: `none` or `auto` |
| `--progress` | `true` | Show distributed model-loading progress |
| `--verbose` | `false` | Show detailed NIBIA planning and Fabric telemetry |
| `--verbose-runtime` | `false` | Show raw llama-server runtime output |
| `--allow-runtime-mismatch` | `false` | Development escape hatch for incompatible llama.cpp source identities |
| `--controller` | `http://127.0.0.1:8080` | Local Controller URL |

The default interactive UI is served at `http://127.0.0.1:8081/`; the local OpenAI-compatible API base is `http://127.0.0.1:8081/v1`.

RUN and SERVE share the same default NIBIA UX vocabulary for Fabric preparation, safe planning, tensor-cache reuse, and model-loading progress. During distributed loading, the live indicator reports observed relay transfer, transfer rate, and elapsed time; it intentionally does not calculate a percentage or ETA from planned remote tensor placement because persistent RPC cache reuse can make actual network transfer dramatically smaller. Successful runs keep the default output concise; `--verbose` exposes NIBIA's detailed planner/telemetry view, while `--verbose-runtime` is reserved for raw llama.cpp logs. Capacity refusals always print the detailed per-node memory breakdown even without `--verbose` so the reason for the refusal remains actionable.

## Advanced Fabric diagnostics

These commands expose lower-level Fabric/runtime controls for troubleshooting and explicit operator control. They are not required for the normal `nibia run` / `nibia serve` path. Run `nibia fabric --help` to print this advanced Fabric command map.

### `nibia fabric status`

```text
nibia fabric status [--controller <url>]
```

Shows llama.cpp-capable nodes, aggregate node-local RAM, coordinator/worker capability, and distributed topology readiness. The reported aggregate memory is capacity across independent node-local memory domains, not a shared address space.

### `nibia fabric devices`

```text
nibia fabric devices [options]
```

Advanced device/runtime discovery for remote llama.cpp workers.

| Flag | Default | Purpose |
| --- | --- | --- |
| `--node` | empty | Single remote Worker name/id |
| `--nodes` | empty | Comma-separated candidate Workers; empty means auto-discover eligible Workers |
| `--auto-start` | `true` | Start managed RPC workers/relays when needed; auto-started resources are cleaned up when the command exits. With `false`, caller-managed workers/relays are preserved. |
| `--node-grace` | `15s` | Grace window for transient Worker recovery |
| `--timeout` | `20s` | Device-discovery timeout |
| `--controller` | `http://127.0.0.1:8080` | Local Controller URL |

### `nibia fabric worker`

Advanced lifecycle control for the managed loopback llama.cpp RPC worker on a selected node.

```text
nibia fabric worker start --node <name-or-id> [options]
nibia fabric worker status --node <name-or-id> [options]
nibia fabric worker stop --node <name-or-id> [options]
```

Common flags:

| Flag | Default | Purpose |
| --- | --- | --- |
| `--node` | required | Target node name/id |
| `--controller` | `http://127.0.0.1:8080` | Local Controller URL |
| `--wait-timeout` | `30s` | Maximum lifecycle wait |

`worker start` additionally supports:

| Flag | Default | Purpose |
| --- | --- | --- |
| `--port` | `50052` | Loopback `ggml-rpc-server` port |
| `--cache` | `false` | Enable worker-local llama.cpp RPC tensor cache |

Raw llama.cpp RPC remains loopback-only; NIBIA carries remote RPC traffic through the authenticated reverse relay.

An intentional `worker stop` or automatic workload cleanup is normal lifecycle, not a worker crash. `Last exit error` is reserved for unexpected process exits; requested teardown does not populate it.

### `nibia fabric relay`

Advanced lifecycle and health control for a selected node's authenticated reverse relay.

```text
nibia fabric relay start --node <name-or-id> [options]
nibia fabric relay status --node <name-or-id> [options]
nibia fabric relay probe --node <name-or-id> [options]
nibia fabric relay stop --node <name-or-id> [options]
```

Common flags:

| Flag | Default | Purpose |
| --- | --- | --- |
| `--node` | required | Target node name/id |
| `--controller` | `http://127.0.0.1:8080` | Local Controller URL |

`relay start` additionally supports `--local-port`, default `55052`, for the Controller-loopback relay endpoint.

## Agent binary

Pair a new node:

```text
nibia-agent --pair <http://primary:8080> --code <pair-code> --name <node-name>
```

Start an already-paired Agent:

```text
nibia-agent
```

Options:

| Flag | Default | Purpose |
| --- | --- | --- |
| `--pair` | empty | Pairing URL |
| `--code` | empty | One-time pairing code |
| `--name` | host-derived/default | Friendly node name |
| `--controller` | paired state | Secure Controller URL; normally reused from paired state |
| `--preferred-address` | auto | Preferred local IPv4 address advertised for Fabric traffic |
| `--relay-address` | Controller host + relay port | mTLS reverse-relay Controller address |
| `--relay-port` | `9443` | Derived reverse-relay Controller port |
| `--relay-pool` | `4` | Number of authenticated ready relay tunnels |
| `--interval` | `5s` | Heartbeat interval |
| `--poll-interval` | `2s` | Lease polling interval |
| `--state-dir` | user Agent state directory | Persistent Agent identity directory |

Pairing state is persistent; do not re-pair a healthy Agent on every start.

## Controller binary

```text
nibia-controller [options]
```

| Flag | Default | Purpose |
| --- | --- | --- |
| `--pair-listen` | `0.0.0.0:8080` | LAN pairing/info listener |
| `--secure-listen` | `0.0.0.0:8443` | mTLS node API listener |
| `--relay-listen` | `0.0.0.0:9443` | mTLS reverse RPC relay listener |
| `--advertise-host` | auto/empty | Host/IP advertised to other nodes |
| `--no-discovery` | `false` | Disable NIBIA LAN multicast discovery |
| `--discovery-include-virtual` | `false` | Include VPN/tunnel/virtual interfaces in discovery |
| `--state-dir` | user Controller state directory | Persistent Controller state directory |

The public Quickstart uses an explicit `--advertise-host <PRIMARY_LAN_IP>` so Worker pairing and secure endpoints are unambiguous on multi-interface machines.

## Scope note

NIBIA Fabric v0.7.0-alpha's public CLI is intentionally limited to the distributed GGUF inference path above. Additional prototype subsystems are intentionally excluded from this release surface.
