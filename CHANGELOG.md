# NIBIA Fabric changelog

## v0.7.0-alpha — Experimental Alpha

- Completes the final exact-artifact acceptance on the frozen public-named macOS arm64, Linux amd64, and Windows amd64 archives; all three platform installs, local Doctor checks, binary-identity checks, three-node RUN/SERVE/API lifecycle gates, and synchronous cleanup checks passed before publication.
- Freezes the public release checksums at `661a0224...` (macOS), `d6622c93...` (Linux), and `f166cb30...` (Windows).
- Keeps RUN and SERVE distributed-loading progress on the same live 750 ms cache-aware renderer, and clarifies stopped relay diagnostics by labeling persistent Agent reverse-mTLS tunnels separately from an active controller-loopback relay while preserving the target node identity.
- Makes the Controller the single liveness authority during lifecycle cleanup: the CLI no longer abandons RPC-stop cleanup on its shorter readiness-staleness threshold, preventing `Runtime cleanup: complete` from racing ahead of still-running remote workers after heavy inference. Cleanup also validates that a successful stop result reports `running=false`, and RUN/SERVE distinguish warning-bearing teardown from fully clean teardown.
- Treats requested managed RPC-worker teardown as normal lifecycle, so `nibia fabric worker status` reserves `Last exit error` for unexpected process exits instead of reporting cross-platform `Process.Kill()` results as failures.
- Adds a checksum-first macOS quarantine preflight to the unsigned Experimental Alpha installer: NIBIA detects quarantine and stops with explicit guidance rather than allowing Gatekeeper to surface a cryptic `Killed: 9`; quarantine is never removed automatically.
- Adds a reproducible release-packaging path that cross-builds with `-trimpath -buildvcs=false`, specializes Unix installers for their exact target platform, emits only the three public platform ZIPs, and writes the SHA-256 manifest.
- Makes Unix uninstall symmetric with Windows by removing only the exact PATH marker/entry that the NIBIA installer added while preserving state/runtime unless purge is explicitly requested.
- Clarifies platform parity: macOS arm64, Linux amd64, and Windows amd64 can all be Primary Nodes, and documents `--preferred-address` for multi-interface Agent starts.
- Unifies the default `nibia run` / `nibia serve` UX around the SERVE-style staged flow, safe-capacity summary, remote tensor planning, relay traffic, and persistent RPC cache/reuse reporting. Detailed NIBIA planner/process telemetry moves behind `--verbose`, while `--verbose-runtime` remains reserved for raw llama.cpp logs; capacity refusals still expand the full per-node memory breakdown automatically.
- Uses the same cache-aware distributed loading indicator in RUN and SERVE: observed relay transfer, transfer rate, and elapsed time with no misleading percentage or ETA against planned remote tensor placement.
- Stops one-shot `nibia run` from echoing the input prompt before the generated response by filtering only a proven initial runtime echo at NIBIA's stdout boundary; a sole matching output line is preserved as a potentially legitimate model response, and interactive `--session` behavior is unchanged.
- Uses `Ready. Run: nibia version` as the post-install handoff and records project copyright as `Copyright 2026 Abel López Almanza` in `NOTICE` without modifying the canonical Apache-2.0 license text.
- Simplifies pairing to `nibia pair` (no redundant `create` action), normalizes remaining help formatting, and removes the test prompt default from `nibia run`; one-shot RUN requires `--prompt`, while `--session` may start without an initial prompt.
- Normalizes the public CLI/build identity to `v0.7.0-alpha` from the physically accepted `alpha.6.18.7` stabilization core.
- Narrows the public CLI, Agent executor, Controller routes, types, and documentation to the validated distributed GGUF inference release surface; earlier prototype RAG, embedding, Ollama-orchestration, synthetic distributed-workload, and generic scheduler CLI surfaces are not shipped.
- Promotes the two primary inference lifecycles to top-level `nibia run` and `nibia serve`; low-level `nibia fabric status/devices/worker/relay` commands remain advanced diagnostics only.
- Ships macOS arm64, Linux amd64, and Windows amd64 release archives with SHA-256 checksums; GitHub provides source archives from the release tag.
- Validates Qwen at multiple sizes plus GPT-OSS 20B and Gemma 3 27B distributed inference.
- Includes bounded SERVE recovery, active-load heartbeat hardening, Windows telemetry recovery, local-only inference defaults, trusted-LAN pairing documentation, and power/sleep guidance.
- Adds concise `nibia`, `nibia --help`, and `nibia help advanced` command maps while preserving the full diagnostic surface in `docs/CLI.md`.
- Renames the advanced diagnostic namespace from `generative` to `fabric`, so inference stays at `nibia run` / `nibia serve` while low-level Fabric controls live under `nibia fabric ...`.
- Normalizes CLI help so `-h` and `--help` work without side effects across command namespaces and leaf commands, and command help renders the documented `--flag` style.
- Aligns the `run` and `serve` default context to `4096`, clarifies Controller/Primary terminology, and removes the misleading displayed `512 MiB` default from the legacy `--reserve-mb` override while preserving adaptive reservation as the actual default.
- Keeps wire protocol `2` and pinned llama.cpp `b10902` / `df03399`.


## v0.7.0-alpha.6.18.6 — SERVE service recovery

- Keeps `nibia generative serve` alive after a selected distributed worker is lost.
- The interrupted request is not replayed; NIBIA enters `RECOVERING` instead of requiring a manual SERVE restart.
- Waits up to 5 minutes by default for the same selected worker capacity to become fresh again (`--recovery-timeout`).
- Recreates managed RPC workers/relays, revalidates runtime parity and device visibility, replans safe capacity, reloads the model, and restores the API automatically.
- `--auto-recover=false` preserves fail-stop behavior for troubleshooting.

## v0.7.0-alpha.6.18.4 — control-plane heartbeat hardening

- Decouples 5-second Agent heartbeats from CPU/RAM/runtime/network/RPC telemetry collection so a slow telemetry probe cannot make a healthy Agent disappear from the Controller.
- Heartbeats now send the latest good telemetry snapshot while the background collector refreshes independently.
- Cached snapshots preserve their original telemetry timestamp, so the existing generative readiness gate can keep a node online while refusing stale capacity for new model placement.
- Adds a dedicated 3-second heartbeat request timeout so a congested control request fails fast and retries on the next tick instead of consuming most of the 30-second offline window.
- Bounds Windows PowerShell/CIM telemetry commands to 5 seconds; slow telemetry degrades metrics temporarily rather than blocking liveness.
- Adds regression coverage proving heartbeats continue while telemetry collection is blocked.
- Protocol remains `2`; existing pairings/state directories remain compatible.

## v0.7.0-alpha.6.18.3 — worker-loss cleanup

- Detects an unavailable cleanup target while RPC-stop/Power-Guard-release jobs are pending and cancels them immediately instead of waiting for a guaranteed timeout.
- Treats cleanup against an already-lost worker as expected fault-recovery behavior rather than emitting misleading cleanup warnings.
- Converts llama.cpp aborts caused by a lost selected worker into a clear NIBIA worker-loss message while still terminating SERVE safely.
- Keeps protocol `2`, pairings, model identity, Ethernet address override, and the alpha.6.18.2 normal-shutdown fixes unchanged.

## v0.7.0-alpha.6.18.2 — cleanup stabilization

- Bounds Linux `systemd-inhibit` teardown and kills the complete helper process group on release.
- Reorders SERVE shutdown so remote RPC workers stop before Power Guard is released.
- Targets the observed `power-guard-release-*` cleanup lease timeout/cancellation while preserving protocol `2` and existing pairings.

## v0.7.0-alpha.6.18.1 — release stabilization

- Continues from the exact Experimental Alpha RC2 source package.
- Changes llama-server/OpenAI model identity from the UI-branded alias to the clean GGUF basename without `.gguf`; display and API model identity now match.
- Adds lifecycle-cleanup placement for pinned RPC-stop and Power-Guard-release jobs so a temporarily stale heartbeat after heavy inference does not make cleanup immediately UNSCHEDULABLE.
- Extends cleanup recovery to 15 seconds and cancels timed-out cleanup jobs to prevent delayed cleanup from affecting a subsequent SERVE session.
- Keeps protocol version `2`, pinned llama.cpp runtime `b10902` / `df03399`, persistent worker-local tensor cache, local-only SERVE defaults, and the RC2 packaging/security baseline.

## v0.7.0 Experimental Alpha — release freeze

- Normalizes the public binary version to `v0.7.0-alpha` while retaining the physically accepted `v0.7.0-alpha.6.17` core behavior.
- Adds Apache-2.0 licensing and a project NOTICE.
- Freezes the public README/Quickstart/security/dependency/troubleshooting surface for the first Experimental Alpha.
- Adds reproducible Wi-Fi-versus-Ethernet benchmark methodology plus measured reference results without making benchmark performance a release blocker.
- Keeps GPU, Android, IoT, training, Hub/cloud, custom UI, model-management automation, and max-capacity/unsafe modes deferred.

## Experimental Alpha packaging RC1 — core v0.7.0-alpha.6.17

- Keeps the physically accepted alpha.6.17 core unchanged; this is a packaging/onboarding candidate, not another inference microversion.
- Adds user-local installers that put `nibia`, `nibia-agent`, and `nibia-controller` on PATH and run managed-runtime setup plus `doctor --local`.
- Adds uninstall helpers that preserve pairings/runtime by default.
- Replaces internal-first onboarding text with public README, Quickstart, Security, Dependencies, Troubleshooting, CONTRIBUTING, release checklist, and explicit manual release blockers.
- Final public macOS archives still require Developer ID signing/notarization.
- Public GitHub publication still requires an owner-selected open-source LICENSE and security-reporting channel.

## v0.7.0-alpha.6.17

- Carries forward alpha.6.16 CPU-only Primary + RPC capacity expansion, including `CPU0`-internal planning, RPC-only llama.cpp offload device lists, `-ngl auto`, `-fit on`, and per-worker fit targets.
- Hardens macOS Power Guard: NIBIA owns a long-lived `caffeinate -i` helper and verifies the actual `PreventUserIdleSystemSleep` assertion with `pmset` before reporting the guard active.
- Adds a Power Guard preflight to `nibia doctor --local`.
- Reworks distributed load UX so planned remote tensors and actual network transfer are separate quantities; cached loads no longer display a false `transferred/planned` progress percentage.
- Disables animated load rendering when `--verbose-runtime` is active so raw llama.cpp logs remain readable.
- Adds `SAFE USABLE` memory to `nibia nodes` using the default adaptive reserve, making the safety margin visible before planning.
- Improves pre-ready runtime failure guidance and bounds remote Power Guard/RPC-stop cleanup waits to reduce long shutdown stalls.
- Adds explicit end-user vs development dependency documentation and VPN/LAN troubleshooting.

## v0.7.0-alpha.6.16

- Enables capacity expansion when the Primary is CPU-only (Linux/Windows) instead of rejecting `CPU0 + RPC` planning.
- Keeps `CPU0` as a NIBIA-only host CPU/RAM execution domain; it is never passed to llama.cpp as an offload device.
- CPU-only distributed execution exposes only selected RPC devices to llama.cpp, keeps the host CPU as fallback, and uses `-ngl auto` + `-fit on` for layer placement.
- Passes NIBIA's per-worker memory reserve to llama.cpp `--fit-target` while preserving minimum-safe-fabric worker selection, secure loopback relays, runtime parity checks, Lazy Worker Activation and persistent RPC tensor cache.
- Planned CPU-primary shares represent the minimum remote model fraction required to keep the host within its safe local RAM budget; actual RPC placement remains llama.cpp-authoritative.
- Accelerator-backed distributed execution (for example macOS Metal + RPC workers) remains unchanged.
- Adds regression coverage proving CPU-only distributed plans never pass the NIBIA pseudo-device `CPU0` to llama.cpp.

## v0.7.0-alpha.6.15

- Added prebuilt NIBIA application packaging for macOS arm64, Linux amd64 and Windows amd64 physical acceptance.
- Added `nibia setup` to bootstrap/verify the pinned managed llama.cpp runtime without Go/CMake/source compilation.
- Added `nibia doctor` with local-only and Controller/fabric validation modes.
- `nibia nodes` now prints Controller version compatibility so stale Controllers are visible immediately.
- No scheduler, tensor-split, memory-policy, secure-relay or SERVE semantics changed.

## v0.7.0-alpha.6.14.4

- Fixes managed llama.cpp runtime telemetry so `nibia nodes` reports the verified runtime identity instead of `unknown` / `UNKNOWN`.
- Agents now trust NIBIA-managed runtime metadata already verified at install time rather than re-running slow `--help` / `--version` probes on every inventory refresh.
- Preserves active probing for external/development llama.cpp runtimes.
- No protocol change; existing pairings and Agent state directories remain valid.

## v0.7.0-alpha.6.14.3

- Harden Windows managed llama.cpp installation against transient ACCESS_DENIED during staging-directory rename.
- Install verified archive before executable health probing, with retry and Windows copy fallback.

## v0.7.0-alpha.6.14.2

- Separates pinned artifact identity (verified SHA-256) from executable health checks.
- Probes the managed `llama-server` first and `llama-cli` as fallback after extraction.
- Extends first-launch runtime probe timeout to 30 seconds for macOS and reports actual loader/startup failures instead of false empty commit mismatches.
- Carries forward process-local macOS/Linux managed-library paths from alpha.6.14.1.

## v0.7.0-alpha.6.14.1

- Fixes NIBIA-managed llama.cpp startup on macOS when official prebuilt binaries need sibling dynamic libraries.
- Preserves safe symlinks/hardlinks while extracting upstream tar archives.
- Uses process-local managed runtime library paths on macOS/Linux; no user shell changes are required.


## v0.7.0-alpha.6.14

- Adds a NIBIA-managed llama.cpp runtime pinned to upstream `b10902` / commit `df03399`.
- Adds `nibia runtime status` and `nibia runtime ensure`.
- Pins official CPU prebuilt assets for macOS/Linux/Windows on amd64/arm64.
- Verifies the downloaded archive SHA-256 before extraction and verifies the extracted binary source identity before use.
- Installs the managed runtime under the user's `~/.nibia/runtime/llama.cpp` tree; no CMake/source compilation is required for the normal product path.
- NIBIA prefers managed binaries for `llama-cli`, `llama-server`, and `ggml-rpc-server`; existing PATH binaries remain a development/backward-compatible fallback.
- Fresh generative commands and Agents can bootstrap the pinned runtime when no llama.cpp runtime is available.
- Protocol remains `2`; existing pairings and Agent state directories are unchanged.

## v0.7.0-alpha.6.13

- Adds compact llama.cpp runtime parity to `nibia nodes`: one Primary runtime identity plus per-node `MATCH` / `MISMATCH` state.
- Keeps SERVE local-only for this Experimental Alpha (`127.0.0.1` / loopback); non-loopback bind requests are rejected before planning/model load.
- Restricts llama-server CORS to `localhost` internally without adding API-auth configuration to the user workflow.
- Cleans SERVE UX: CLI model names omit `.gguf`, the internal `· nibia` alias is not presented as model identity, and the API line is labeled `OpenAI API base`.


## v0.7.0-alpha.6.12

- Makes distributed model-loading progress visible by default in SERVE with one aggregate progress bar, transfer amount/rate, elapsed time and ETA when measurable.
- Adds a lightweight local-model loading indicator for local SERVE.
- Adds `--open-ui=loading|ready|never`; default is `loading`, while `never` supports headless/SSH operation.
- Adds a discreet default llama.cpp model alias derived from the GGUF filename and suffixed with `· nibia`; `--model-alias` can override it.
- Labels the backend explicitly as `llama.cpp runtime` and separates runtime parity status.
- Hides raw llama-server startup logs by default for a cleaner user-facing lifecycle; `--verbose-runtime` restores them.
- Keeps the progress bar aggregate by default to avoid per-node output saturation.

## v0.7.0-alpha.6.11

- Enables llama.cpp RPC worker-local persistent tensor caching by default for automatic distributed NIBIA fabrics.
- Reuses the existing managed RPC worker `-c` capability instead of copying GGUF files manually to workers.
- Restarts an already-running on-demand RPC worker when its cache mode does not match the automatic fabric policy.
- Keeps Agents persistent while RPC workers/relays remain ephemeral; stopping a workload does not erase the worker-local tensor cache.
- Adds visible SERVE/runtime messaging: `RPC tensor cache: enabled (persistent, worker-local)`.
- Acceptance focuses on one cold distributed load followed by one warm load of the same model/worker; speedup is measured, not assumed.

## v0.7.0-alpha.6.10

- Replaces the default fixed 512 MiB/device planning reserve with adaptive per-node memory reservation: 10% of physical RAM, minimum 512 MiB, maximum 4096 MiB.
- Adds `--memory-reserve` per-node overrides supporting fixed MiB/GiB or percentages, including the Primary Node.
- Keeps `--reserve-mb` as an explicit backward-compatible global fixed override.
- Plan output now shows the resolved reserve for every selected node and the active reserve policy.
- Adds a Primary-local SERVE Instance Guard: a second `nibia generative serve` is rejected before fabric preparation/planning even if a different Web/API port is requested.
- The SERVE guard uses an OS-owned loopback listener (`127.0.0.1:55051`), so process exit/crash does not leave stale lock files.
- Carries forward alpha.6.9.x readiness and remote-fabric cleanup hardening.
- Protocol remains `2`; existing pairings and Agent state directories remain reusable.

## v0.7.0-alpha.6.9.2

- Tears down ephemeral controller-side RPC relays and managed RPC workers when SERVE exits or fails after fabric activation.
- Frees loopback relay ports for immediate reuse by the next workload.
- OFFLINE nodes no longer present stale CPU/RAM values as current telemetry in `nibia nodes`.

## v0.7.0-alpha.6.9.1

- Refines Worker Readiness Gate semantics: `--node` / `--nodes` are candidate sets, not mandatory-worker sets.
- SERVE proceeds with a fresh minimum-safe-fabric subset when current safe capacity is sufficient, even if another allowed candidate is recovering.
- Increases required-capacity recovery grace from 8s to 15s.
- Adds graceful Agent shutdown notification so `Ctrl+C` can mark a node OFFLINE immediately instead of waiting for the missed-heartbeat timeout.

## v0.7.0-alpha.6.9

- Added Worker Readiness Gate for generative lifecycles. Remote workers must have a fresh heartbeat and fresh telemetry before they are considered schedulable.
- Requested workers that are waking/recovering get the existing grace window and a clear `RECOVERING` message instead of being scheduled from stale controller state.
- Auto-discovered stale workers are excluded from candidate capacity until fresh Agent telemetry returns.
- Generative fabric summaries now expose heartbeat and telemetry timestamps used by the gate.

## v0.7.0-alpha.6.8

- Carries forward **Lazy Worker Activation** from alpha.6.7: local-fit SERVE workloads do not start remote RPC workers/tunnels, and distributed SERVE activates the minimum remote subset first.
- Adds **Active Job Power Guard** for generative ONE-SHOT, SESSION, and SERVE execution.
- Primary Node sleep inhibition is acquired automatically while llama.cpp is actively executing and released on completion or Ctrl+C.
- Selected remote workers receive managed power-guard acquire/release jobs through their existing authenticated Agent connection.
- Cross-platform backends: macOS `caffeinate`, Windows `SetThreadExecutionState`, Linux `systemd-inhibit`.
- Power-guard cleanup is best-effort and scoped to the active workload; NIBIA does not permanently change system sleep settings.
- Adds Agent capability `task-power-guard` and lifecycle regression coverage.
- Protocol remains `2`; existing pairings/state directories remain reusable after updating Agents.

## v0.7.0-alpha.6.6

- Adds experimental `nibia generative serve` lifecycle backed by persistent `llama-server`.
- Reuses N-node secure RPC fabric, runtime parity checks, OS-memory-safe planning and tensor split.
- Local-only server binding by default (`127.0.0.1:8081`).
- Adds cross-platform build/test coverage for SERVE argument construction.


## v0.7.0-alpha.6.5

### Fixed
- Primary-node planning now clamps local llama.cpp capacity to `min(runtime free, OS-available memory)` before subtracting the normal reserve, matching the safety rule already used for remote workers.
- Local plan output now exposes `runtime-free`, `OS-avail`, `basis`, and `usable` just like remote rows.
- Prevents a local Metal/CPU runtime from making the fabric appear large enough when the Primary Node is already under real host-memory pressure or swap use.

### Architecture
- The default Simple Mode remains one Primary Node hosting Controller + Coordinator + Agent + CLI + local compute, with `n` Remote Workers.
- The Primary Node safety logic is platform-neutral: macOS, Linux, and Windows use normalized Agent OS-memory telemetry.
- Controller and Coordinator remain separate internal roles even when colocated by default.

### Preserved
- Wire protocol remains `2`; existing pairings, Agent state, relay state, and pinned llama.cpp runtimes are reusable.
- No llama.cpp rebuild is required for this NIBIA-only planner change.

## v0.7.0-alpha.6.4

### Hardened
- Remote CPU RPC planning now clamps each worker to `min(runtime free, OS-available memory)` before subtracting the normal reserve.
- N-node plan output exposes runtime-free, OS-available, capacity-basis, and usable MiB per remote worker.
- Requested workers get an 8-second transient OFFLINE grace/retry window before a run is rejected.
- `nibia nodes` now labels physical/logical CPU topology explicitly as `P/L`.
- Cross-platform memory telemetry now exposes normalized headroom plus `LOW / MODERATE / HIGH / CRITICAL` pressure severity; Linux incorporates PSI when available.
- Device discovery explains that zero-memory BLAS/Accelerate entries are compute backends and are excluded from capacity planning.

### Physical milestone carried forward
- Qwen3-30B-A3B-Q4_K_M (17.28 GiB) was rejected on Mac+Linux and Mac+Windows, then completed on Mac+Linux+Windows with the normal 512 MiB/device reserve.
- This is the first physical NIBIA 3-node capacity-expansion proof.

### Preserved
- Wire protocol remains `2`; existing pairings and state directories remain valid.
- Windows llama.cpp does not need to be rebuilt for alpha.6.4.

## v0.7.0-alpha.6.3

### Added
- Runtime source-identity compatibility for heterogeneous llama.cpp builds: version + Git commit are compared while local build counters are ignored.
- Commit-prefix normalization allows the same source commit to match when one platform reports a longer short hash (for example `df03399` vs `df03399b8`).
- Loading UX now prints an explicit aggregate remote target and per-worker target breakdown.
- Multi-worker progress is labeled `Remote aggregate` instead of implying the aggregate byte count belongs to one worker.
- Regression coverage for heterogeneous runtime parity and aggregate/per-worker remote target reporting.

### Physical milestone carried forward
- v0.7.0-alpha.6.2.2 achieved a successful single distributed inference across Mac (`MTL0`) + Linux (`RPC0`) + Windows (`RPC1`).
- Qwen3-14B-Q6_K completed with 3 selected nodes, 0 relay bridge failures, and process-scoped CPU/RSS telemetry for all three inference processes.
- This is a 3-node heterogeneous distributed-inference proof; a true 3-node capacity-expansion proof with a model that cannot fit on either two-node combination remains the next milestone.

### Packaging direction
- Windows source compilation remains supported for development, but the intended public-release onboarding path is pinned prebuilt/verified llama.cpp RPC runtime delivery rather than requiring users to compile locally.


## v0.7.0-alpha.6.2.2

### Fixed
- Automatic generative runs now launch llama.cpp with `--simple-io` so embedded subprocess output follows capturable stdout/stderr paths instead of terminal-oriented UI paths.
- ONE-SHOT runs no longer attach stdin to the controlling terminal; SESSION mode keeps stdin for follow-up prompts.
- Runtime readiness can no longer be triggered by stderr. This prevents independently ordered stderr prompt/banner output from opening the NIBIA output gate before stdout reaches the real interactive boundary.
- Added regression tests for stderr readiness isolation and delayed startup-stderr suppression.

### Preserved
- N-node minimum-sufficient subset planning.
- Dynamic tensor split from detected usable device memory.
- Process-scoped coordinator and RPC-worker CPU/RSS telemetry.
- Linux physical-LAN preference and Windows secure inventory.
- Control-plane heartbeat/lease separation and relay reconnection behavior.
- Protocol remains `2`; existing pairings/state directories remain valid.

### Physical acceptance status
- Mac + Linux capacity expansion: PASS previously.
- Windows secure join/inventory: PASS previously.
- alpha.6.2.1 process telemetry, LAN selection, cleanup, and three-node control-plane stability: PASS in physical testing.
- alpha.6.2.1 loading UX: FAIL; llama.cpp terminal UI still bypassed the output gate.
- alpha.6.2.2 subprocess-safe UX: pending physical acceptance.
- Windows llama.cpp RPC worker / three-node inference: next milestone after this hotfix passes.

## v0.7.0-alpha.6.10

- Fix SERVE teardown leaving controller loopback RPC relays bound after Ctrl+C.
- Tear down ephemeral RPC relays before managed RPC workers so relay ports are immediately reusable.
- Clean partially prepared remote fabric on relay/worker preparation failures.
- Clean remote fabric on SERVE planning, runtime-parity, insufficient-capacity, startup, normal-exit, and interrupt paths.
- Add explicit shutdown feedback while SERVE cleanup is in progress.
