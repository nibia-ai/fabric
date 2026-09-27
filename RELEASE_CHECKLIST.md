# NIBIA Fabric v0.7.0 Experimental Alpha release checklist

## Core — physically accepted

The following behavior was physically validated during the stabilization line and remains the release baseline:

- [x] macOS Apple Silicon local execution.
- [x] Linux amd64 CPU-only Primary local execution.
- [x] Windows amd64 CPU-only Primary local execution.
- [x] Linux/Windows CPU Primary + remote RPC capacity expansion.
- [x] Heterogeneous three-node distributed inference.
- [x] Minimum Safe Fabric / Lazy Worker Activation.
- [x] Adaptive SAFE memory reserve and visible `SAFE USABLE` capacity.
- [x] Persistent RPC tensor cache/reuse correctness.
- [x] Runtime parity and pinned managed llama.cpp.
- [x] Workload-scoped Power Guard on macOS/Linux/Windows.
- [x] Repeated SERVE lifecycle and clean teardown.
- [x] Worker absent before load fails safely.
- [x] Selected-worker loss during inference fails the active request safely.
- [x] Automatic SERVE service recovery after worker reconnection.
- [x] Idle and sustained-load control-plane stability.
- [x] Active-load Windows heartbeat hardening.
- [x] Qwen validation at multiple sizes, including exhaustive Qwen3-30B-A3B release testing.
- [x] GPT-OSS 20B MXFP4 cross-family distributed smoke.
- [x] Gemma 3 27B IT Q4_K_M cross-family distributed smoke.
- [x] Context 8192 / 16384 / 32768 physical validation on the reference fabric.
- [x] Open WebUI interoperability.
- [x] OpenClaw interoperability.

## Final stabilization — accepted baseline

- [x] Windows static CPU/core telemetry recovery.
- [x] Distributed-load relay traffic wording corrected so overhead is not presented as tensor payload.
- [x] Power/sleep operational guidance added.
- [x] Trusted-LAN pairing bootstrap documented.
- [x] Stabilization-core exact-artifact acceptance: all three Agents + Controller MATCH, one distributed SERVE/API inference, clean Ctrl+C, 3/3 READY, API closed.

## Public-scope cleanup

Before publication, the source was narrowed to the functionality that belongs to this Experimental Alpha.

- [x] Public CLI limited to setup/doctor/discovery/pairing/trust/runtime/node inspection and distributed GGUF generative execution.
- [x] Stale standalone `generative plan` command removed; `run`/`serve` remain authoritative for adaptive SAFE planning.
- [x] Prototype RAG, embedding, Ollama orchestration, synthetic distributed-workload, model-registry, and generic scheduler CLI surfaces removed.
- [x] Agent executor limited to managed llama.cpp RPC lifecycle and Power Guard workloads.
- [x] Controller public/local routes narrowed to the endpoints required by the current Fabric lifecycle.
- [x] Dead prototype types/stores/packages removed.
- [x] Makefile and CLI documentation reconciled with the actual release surface.
- [x] `go test ./...` passes after cleanup.
- [x] `go vet ./...` passes after cleanup.

Because this cleanup and final hardening changed release binaries/installers, the exact public-named release artifacts were reinstalled and physically accepted on macOS arm64, Linux amd64, and Windows amd64 before publication. Historical benchmark, long-context, client-integration, and recovery campaigns were not repeated because the final gate did not reveal a data-plane regression.

## Final pre-public hardening

- [x] RUN/SERVE default UX reconciled around the same staged safe-capacity, tensor-cache/reuse, and cache-aware loading vocabulary.
- [x] RUN/SERVE live distributed-loading progress uses the same cache-aware formatter and cadence; advanced stopped-relay status preserves node identity and distinguishes persistent Agent reverse tunnels from the inactive local relay.
- [x] One-shot RUN suppresses only a proven initial runtime prompt echo while preserving legitimate model output.
- [x] Requested managed RPC-worker teardown no longer populates `Last exit error`; unexpected process exits remain visible.
- [x] macOS unsigned-alpha installer detects quarantine and stops with checksum-first guidance instead of removing quarantine automatically.
- [x] Documentation states that macOS arm64, Linux amd64, and Windows amd64 can all be Primary Nodes.
- [x] Multi-interface/VPN guidance documents `--preferred-address <NODE_LAN_IP>` and LAN-access requirements.
- [x] `gofmt`, `go test ./...`, `go vet ./...`, and race testing pass before packaging the final public-named artifacts.

## Final exact-artifact gate — required before publication

- [x] Verify SHA-256 for the exact public-named macOS, Linux, and Windows archives.
- [x] On macOS, when quarantine is present, installer preflight stops before copying/executing binaries and prints checksum-first remediation; after verified `xattr` removal, install succeeds.
- [x] Install the exact public-named macOS arm64 archive on the reference Mac.
- [x] Install the exact public-named Linux amd64 archive on the reference Linux node.
- [x] Install the exact public-named Windows amd64 archive on the reference Windows node.
- [x] Installed `nibia`, `nibia-agent`, and `nibia-controller` hashes match the binaries inside the extracted accepted archive on each platform.
- [x] `nibia version` reports `v0.7.0-alpha` / protocol `2` on all three platforms.
- [x] `nibia doctor --local` passes on all three installed platforms.
- [x] Controller + three Agents form the Ethernet reference Fabric; `nibia nodes` shows 3/3 fresh/READY, Agent MATCH, and runtime `df03399` MATCH.
- [x] `nibia fabric devices` sees the Primary local device plus both remote RPC devices through loopback relays, then leaves both remote managed workers stopped after diagnostic cleanup.
- [x] One Qwen3-30B-A3B distributed `nibia run` at ctx 8192 returns exactly `NIBIA RELEASE OK`, exits 0, reports `Runtime cleanup: complete`, and an immediate first post-RUN status check (no grace sleep) shows both remote managed workers already `Running: false` / PID 0 without false exit errors; 3/3 nodes return READY/MATCH.
- [x] One Qwen3-30B-A3B distributed `nibia serve` at ctx 8192 reaches Model ready and exposes localhost-only port 8081.
- [x] `/health` returns `{"status":"ok"}` while SERVE is active.
- [x] One `/v1/chat/completions` request returns exactly `NIBIA RELEASE OK`.
- [x] Ctrl+C reports runtime cleanup complete and returns all persistent Agents to READY.
- [x] Port 8081 is closed after SERVE teardown and no `llama-server`, `llama-cli`, or managed `ggml-rpc-server` process is left running.
- [x] `nibia fabric worker status` after intentional cleanup reports `Running: false` without a false `Last exit error` on Linux and Windows.
- [x] Reboot/restart of an Agent preserves pairing/runtime identity and the node returns READY/MATCH; transient absence during restart is treated as expected.

## Public-release packaging

- [x] Public version normalized to `v0.7.0-alpha`.
- [x] Exact public-named per-platform archives prepared from the final hardening source via `packaging/build-release.sh` / `make release-assets`.
- [x] User-local install/uninstall scripts included.
- [x] README / Quickstart / CLI / Architecture / Security / Dependencies / Troubleshooting / CONTRIBUTING / GOVERNANCE / CHANGELOG / RELEASE_NOTES present.
- [x] Apache-2.0 LICENSE + NOTICE present.
- [x] Final archive SHA-256 checksums frozen after the exact-artifact gate passed; accepted asset bytes are immutable for `v0.7.0-alpha`.

## Public repository

- [ ] Enable GitHub Private Vulnerability Reporting before or immediately with publication.
- [ ] Create Git tag `v0.7.0-alpha` from the exact source used for the accepted artifacts.
- [ ] Create release title **NIBIA Fabric v0.7.0 Experimental Alpha**.
- [ ] Attach macOS arm64, Linux amd64, Windows amd64, and SHA-256 checksum assets. GitHub generates source-code archives from the tag automatically.

## macOS signing/notarization

- [ ] Developer ID signing.
- [ ] Apple notarization/stapling.

For the first Experimental Alpha, signing/notarization may be deferred if the release is explicitly labeled unsigned, SHA-256 verification is published, and the macOS quarantine note remains in the Quickstart. Signing/notarization should be completed before a broader non-experimental distribution.

## Benchmark evidence

- [x] Frozen Wi-Fi-versus-Gigabit-Ethernet A/B completed and documented.
- [x] Three-node Gigabit Ethernet capacity-expansion benchmark completed and documented.

See [docs/BENCHMARKING.md](docs/BENCHMARKING.md).
