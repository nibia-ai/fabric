# NIBIA Fabric release roadmap

NIBIA Fabric is a CPU-first heterogeneous local AI capacity-expansion fabric. This document intentionally tracks the immediate public-alpha release line rather than the separate longer-term product roadmap.

## v0.7.0 Experimental Alpha — release scope

The first public release contains:

1. N-node heterogeneous fabric and authenticated reverse RPC relay.
2. Minimum Safe Fabric / Lazy Worker Activation.
3. SAFE memory reservation with visible `SAFE USABLE` capacity.
4. Worker readiness, bounded service recovery, lifecycle cleanup, and workload-scoped Power Guard.
5. Persistent RPC tensor cache with explicit load/relay reporting.
6. Local-only SERVE defaults and OpenAI-compatible API.
7. Pinned precompiled managed llama.cpp runtime with SHA-256 and runtime parity checks.
8. Prebuilt NIBIA Fabric binaries, installers, `nibia setup`, and `nibia doctor`.
9. CPU-only Primary execution on Linux/Windows and local Metal execution on Apple Silicon.
10. Multi-size Qwen plus GPT-OSS and Gemma distributed validation on heterogeneous physical hardware.

The inference/runtime scope is fixed for publication. A pre-public cleanup removed prototype surfaces outside this scope, followed by CLI/installer/lifecycle hardening. The final public-named macOS, Linux, and Windows artifacts completed the narrow exact-artifact acceptance gate and are frozen for publication.

## Publication steps

- [x] Run the narrow final acceptance gate from the exact public-named platform archives.
- [x] Freeze the repository snapshot and release checksums after that gate passes and a docs-only closure rebuild confirms identical release binaries.
- [ ] Enable GitHub Private Vulnerability Reporting.
- [ ] Publish tag `v0.7.0-alpha` and the macOS/Linux/Windows release assets plus SHA-256 checksums. GitHub will generate source-code archives from the tag.
- [ ] Developer ID signing/notarization can follow if the first Experimental Alpha is clearly published as unsigned with checksum-first macOS instructions.

## Benchmark evidence included with the release

The release documentation includes a measured Wi-Fi-versus-Gigabit-Ethernet A/B using Qwen3-14B Q6_K and a three-node Gigabit Ethernet capacity-expansion benchmark using Qwen3-30B-A3B Q4_K_M. These measurements document the physically validated stabilization core; they are not release gates and should not be generalized beyond the recorded hardware/topology.

See [BENCHMARKING.md](BENCHMARKING.md).

## After the first public release

Post-alpha work should be driven by measured correctness, usability, and performance feedback. Candidate areas include planner/performance optimization, tensor-cache miss-path optimization, packaging/signing improvements, and the separately maintained longer-term NIBIA product roadmap.

NIBIA Fabric never describes independent node memory as physical shared RAM.
