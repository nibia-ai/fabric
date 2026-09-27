# NIBIA Fabric v0.7.0 Experimental Alpha

NIBIA Fabric v0.7.0-alpha is the first public Experimental Alpha of NIBIA's CPU-first local AI compute fabric.

A Primary computer can combine its own local compute/memory with safe capacity from other computers on the same trusted LAN so larger GGUF models can run across heterogeneous everyday hardware.

## Release identity

- Public CLI version: `v0.7.0-alpha`
- NIBIA wire protocol: `2`
- Managed llama.cpp: `b10902`, commit `df03399`
- License: Apache-2.0
- The data-plane stabilization baseline originated in the `alpha.6.18.7` line. Pre-public candidates then narrowed the release surface and hardened CLI, lifecycle diagnostics, installers, and documentation without changing wire protocol `2` or the pinned llama.cpp runtime.

## Highlights

- N-node heterogeneous CPU and RAM capacity expansion.
- macOS Apple Silicon, Linux amd64, and Windows amd64 nodes; any supported platform can be the Primary Node.
- Minimum Safe Fabric / Lazy Worker Activation.
- Adaptive SAFE memory reserve with visible `SAFE USABLE` capacity.
- Linux/Windows CPU Primary execution and Apple Silicon Metal local execution.
- On-demand llama.cpp RPC workers carried through authenticated reverse mTLS relays.
- Raw llama.cpp RPC remains loopback-only on Workers.
- Pinned and SHA-256-verified precompiled llama.cpp runtime.
- Persistent worker-local RPC tensor cache.
- Workload-scoped Power Guard on macOS, Linux, and Windows.
- Local Web UI plus OpenAI-compatible API.
- Automatic bounded SERVE service recovery after selected-worker loss and reconnection.
- Prebuilt per-platform archives and user-local installers, including checksum-first macOS quarantine guidance for an unsigned Experimental Alpha.
- Focused user CLI with top-level `nibia run` for one-shot/terminal inference and `nibia serve` for persistent Web UI/API serving; lower-level Fabric diagnostics live under `nibia fabric status/devices/worker/relay`.
- Consistent `-h` / `--help` behavior across user and advanced command namespaces, with side-effect-free help and documented `--flag` rendering.
- Simplified `nibia pair` pairing-code creation and explicit `nibia run` prompt semantics: one-shot RUN requires `--prompt`; interactive `--session` may start without one.
- Consistent RUN/SERVE staged UX with shared safe-capacity and tensor-cache/reuse reporting. Cache-aware distributed loading reports observed relay transfer, rate, and elapsed time rather than a misleading percentage/ETA against planned remote tensor placement. `--verbose` exposes detailed NIBIA planning/telemetry; `--verbose-runtime` remains the raw llama.cpp debug surface.
- RUN and SERVE use the same live cache-aware loading cadence. When an advanced relay is stopped, `relay status` distinguishes the still-ready Agent reverse-mTLS tunnel pool from the inactive controller-loopback relay and retains the node name.
- Managed RPC worker diagnostics distinguish requested cleanup from unexpected exits, so an intentional worker stop is not reported as `Last exit error`.
- RUN/SERVE lifecycle cleanup waits for the Controller/Agent cleanup jobs instead of applying a stricter CLI heartbeat-staleness cutoff; `Runtime cleanup: complete` is reserved for warning-free teardown.
- One-shot/terminal RUN and advanced device discovery now tear down transient managed RPC workers and authenticated relays on command completion or failure instead of leaving diagnostic/workload processes behind.

## Final exact-artifact acceptance

The exact public-named macOS arm64, Linux amd64, and Windows amd64 archives were reinstalled on the reference hardware before publication. `nibia version` / protocol `2`, local Doctor, installed-binary identity, three-node Fabric parity, distributed RUN, localhost-only SERVE/API, and synchronous lifecycle cleanup all passed on the frozen release bytes.

Frozen release-asset SHA-256 values:

- `661a02243a946e0539ad7eb202b43d157284e3078389ec6252b15e80503fbb17` — `nibia-v0.7.0-alpha-macos-arm64.zip`
- `d6622c93c597f3422d89d836ed712a60578a16d54a731b9ad28633583c706038` — `nibia-v0.7.0-alpha-linux-amd64.zip`
- `f166cb3078f035ad8e46a2209ea1a266e519b8c194368144a83a192e3e35c898` — `nibia-v0.7.0-alpha-windows-amd64.zip`

Publication note (2026-09-27): the checksum values in the release notes were corrected after publication to match the already-published release assets and SHA-256 manifest. The release assets and tag were not replaced or modified.

The macOS archive remains an unsigned Experimental Alpha; the installer therefore requires checksum verification before documented quarantine removal when Gatekeeper quarantine is present.

## Physical validation matrix

The release was validated on a heterogeneous three-machine fabric consisting of Apple Silicon macOS, amd64 Linux, and amd64 Windows nodes over a local LAN.

### Qwen family

- **Qwen3 4B Q4_K_M** — small-model/local and distributed execution coverage during development.
- **Qwen3 14B Q6_K** — successful heterogeneous distributed inference, including three-node execution.
- **Qwen3 30B-A3B Q4_K_M** — primary release-validation workload. Validated for capacity expansion, repeated lifecycle/cleanup, selected-worker fault handling, automatic service recovery, idle/load stability, OpenAI-compatible serving, and contexts of 8192, 16384, and 32768 when the reference fabric had sufficient SAFE memory. Open WebUI and OpenClaw client validation is listed separately below.

### Cross-family release smoke

- **GPT-OSS 20B MXFP4** — capacity-expanding distributed load and real OpenAI-compatible inference.
- **Gemma 3 27B IT Q4_K_M** — capacity-expanding dense-model distributed load and real OpenAI-compatible inference.

Other llama.cpp-compatible GGUF models may work but are not necessarily validated in this release.

## Reference benchmark highlights

- **Qwen3-30B-A3B / three-node Gigabit Ethernet:** 17.28 GiB model; 9.38 GiB first-load transfer at 76.4 MiB/s; model ready in 2:06; 9.337 tok/s median across three no-thinking generation runs.
- **Qwen3-14B Q6_K / Mac + Windows A/B:** model ready 65 s on Wi-Fi versus 27 s on Gigabit Ethernet; median generation throughput 3.47 versus 4.57 tok/s respectively in the measured topology.
- **Warm-cache stabilization acceptance:** Qwen3-30B-A3B planned 10.80 GiB of remote tensor payload while only 0.32 GiB of relay traffic was observed, avoiding an estimated ~10.48 GiB of repeated transfer. The residual cache-miss path remains a known post-alpha optimization target.

See [docs/BENCHMARKING.md](docs/BENCHMARKING.md) for hardware, raw run values, methodology, and interpretation caveats.

## Client compatibility validated

- [llama.cpp](https://github.com/ggml-org/llama.cpp) built-in/local Web UI — the default interactive UI served by NIBIA.
- OpenAI-compatible `/v1/models`.
- OpenAI-compatible `/v1/chat/completions`.
- [Open WebUI](https://github.com/open-webui/open-webui) — physically validated against NIBIA Fabric's local OpenAI-compatible API, including release tests at 8192 and 16384 context sizes.
- [OpenClaw](https://github.com/openclaw/openclaw) — physically validated in local mode with NIBIA Fabric configured as an OpenAI-compatible provider at an 8192-token context window.

Open WebUI and OpenClaw are optional independent clients, not required NIBIA dependencies. Other OpenAI-compatible clients may work but are not necessarily validated in this release.

The OpenAI-compatible endpoint remains local to the Primary by default: `http://127.0.0.1:8081/v1`.

## Recovery semantics

If a selected distributed worker disappears during an active generation, the active request is interrupted rather than continuing with an unsafe partial fabric.

By default, SERVE remains alive for a bounded recovery window, waits for the selected capacity to return, rebuilds the remote RPC fabric, reloads the model, and restores the API automatically when possible.

This release does **not** implement transparent request continuation, request replay, distributed KV-cache replication, or migration of an in-flight generation.

## Known limitations

- **Experimental Alpha:** CLI/API/state details may change.
- **Release scope:** this alpha ships the distributed GGUF inference path. RAG, embedding pipelines, Ollama orchestration, and generic synthetic distributed workloads are not part of this release.
- **Trusted LAN only:** pairing bootstrap is not designed for hostile/public networks.
- **Local-only SERVE:** the first alpha intentionally rejects non-loopback inference binding.
- **CPU-first:** GPU pooling is not part of this release.
- **Inference only:** LoRA/QLoRA training is not part of this alpha.
- **Power policy:** Power Guard cannot override every critical-battery, lid-close, firmware, manual-sleep, or administrator policy.
- **Model provenance:** managed llama.cpp downloads are verified; arbitrary GGUF model files are not signed/verified by NIBIA.
- **Capacity-first scheduler:** Minimum Safe Fabric prioritizes safe placement; more nodes do not necessarily mean higher tokens/s.
- **Tensor-cache performance:** persistent RPC tensor reuse is functional, but some warm loads with small residual misses can still spend disproportionate time in cache resolution/relay work. This is a post-alpha performance optimization item, not a correctness blocker.
- **macOS signing:** if the published Experimental Alpha archive is not yet Developer ID signed/notarized, verify the SHA-256 first and follow the documented quarantine-removal step only for the verified archive.

## Networking guidance

Ethernet is recommended for larger distributed models when practical. Wi-Fi is supported. VPNs can block or reroute LAN traffic; enable local-network access or disable the VPN when troubleshooting fabric reachability. On multi-interface nodes, `--preferred-address <NODE_LAN_IP>` can pin the Agent's advertised Fabric address to the intended physical LAN interface.

## Power guidance

For long workloads, keep laptops connected to external power when practical and configure them not to auto-sleep. NIBIA Power Guard is an additional workload-scoped safeguard, not a replacement for host power policy.
