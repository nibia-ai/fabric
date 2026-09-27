# NIBIA Fabric benchmarking

NIBIA benchmark results should be read as **reference measurements on a specific heterogeneous fabric**, not as guaranteed performance for other hardware. Distributed inference can be limited by CPU speed, memory bandwidth, model partitioning, cache state, latency, and network throughput.

## Reference hardware used during v0.7.0-alpha validation

The main physical validation fabric used three everyday computers:

- **Primary:** MacBook Pro, Apple M2 Pro, 16 GB RAM, macOS arm64.
- **Windows Worker:** 12th Gen Intel Core i5-1235U, 10 physical / 12 logical cores reported by NIBIA, ~15.7 GB visible RAM, Windows amd64.
- **Linux Worker:** AMD Ryzen 5 5500U, 6 physical / 12 logical cores, ~5.6 GB visible RAM, Linux amd64.

For the wired tests, the reference lab used a **5-port Gigabit Ethernet switch** with **Cat8 Ethernet cabling**. The measured network remained **Gigabit Ethernet**; the Cat8 designation describes the cable used, not a 10 Gb/s measured fabric.

Reference networking hardware used in the validation lab:

| Role | Hardware used | Public product reference |
| --- | --- | --- |
| Ethernet switch | Tenda SG105, unmanaged Gigabit Ethernet switch, 5 ports | [Amazon MX — ASIN B01K1JUE1E](https://www.amazon.com.mx/dp/B01K1JUE1E) |
| Ethernet cable | MCSWSEE Cat8 RJ45 Ethernet cable, 2 m | [Amazon MX — ASIN B0DX1WL5ZM](https://www.amazon.com.mx/dp/B0DX1WL5ZM) |
| Mac Ethernet adapter | Bomsirus USB-C to Ethernet adapter, listed for 1000/100 Mbps | [Amazon MX — ASIN B0FGJ6GZ45](https://www.amazon.com.mx/dp/B0FGJ6GZ45) |
| Linux Ethernet adapter | Bomsirus USB-A to Ethernet adapter, listed for 1000/100 Mbps | [Amazon MX — ASIN B0FYM6JF2H](https://www.amazon.com.mx/dp/B0FYM6JF2H) |

These components document the **reference lab**, not NIBIA hardware requirements or endorsements. Equivalent networking hardware can be used. Retail prices are intentionally not frozen here because marketplace pricing changes and the public listings do not expose a stable, directly verifiable price for every item. If cost is reported in a future reproducibility record, it should use the actual purchase/order price and purchase date rather than a later marketplace price.

Wi-Fi remained supported throughout the release. The Wi-Fi/Ethernet A/B below used the same Mac Primary and Windows Worker with the same model and context so the effect of network transport could be observed directly.

## Measured Wi-Fi vs Gigabit Ethernet A/B

This comparison used:

```text
Primary:      Mac M2 Pro
Worker:       Windows amd64
Model:        Qwen3-14B-Q6_K.gguf
Model size:   11.29 GiB
Context:      4096
Lifecycle:    SERVE
Tensor cache: enabled
```

The measured results were:

| Metric | Wi-Fi | Gigabit Ethernet |
| --- | ---: | ---: |
| RTT min / avg / max / variation | 18.949 / 64.620 / 118.758 / 33.146 ms | 1.493 / 1.628 / 1.790 / 0.121 ms |
| Planned node shares | Mac 65.4% / Windows 34.6% | Mac 67.8% / Windows 32.2% |
| Remote tensors planned | 3.91 GiB | 3.63 GiB |
| Data transferred during measured load | 0.11 GiB | 0.10 GiB |
| Average transfer rate | 1.7 MiB/s | 3.7 MiB/s |
| Model ready | 65 s | 27 s |
| Prompt throughput runs | 14.08, 13.66 tok/s | 17.49, 20.25, 21.14 tok/s |
| Generation throughput runs | 3.36, 3.58 tok/s | 4.12, 4.57, 4.61 tok/s |
| Median prompt throughput | 13.87 tok/s (2 runs) | 20.25 tok/s (3 runs) |
| Median generation throughput | 3.47 tok/s (2 runs) | 4.57 tok/s (3 runs) |

In this measured setup, Gigabit Ethernet reduced model-ready time from **65 s to 27 s** and increased median generation throughput from **3.47 tok/s to 4.57 tok/s**. These are measurements from this topology, not a claim that Ethernet produces the same improvement on every NIBIA fabric.

The planned shares differed slightly because live safe memory availability changed between runs. The comparison should therefore be treated as a practical A/B measurement rather than a laboratory-isolated network benchmark.

## Three-node Gigabit Ethernet capacity-expansion benchmark

The primary release benchmark used the full three-node wired fabric:

```text
Model:        Qwen3-30B-A3B-Q4_K_M.gguf
Model size:   17.28 GiB
Context:      4096
Primary:      Mac M2 Pro / Metal
Workers:      Windows amd64 + Linux amd64
Network:      Gigabit Ethernet
Policy:       SAFE / Minimum Safe Fabric
```

Measured planning and first-load result:

| Metric | Result |
| --- | ---: |
| Mac planned share | 43.9% |
| Windows planned share | 33.8% |
| Linux planned share | 22.3% |
| Aggregate planned capacity | 17.79 GiB |
| Remote tensors planned | 9.69 GiB |
| First-load network transfer | 9.38 GiB |
| Average first-load transfer rate | 76.4 MiB/s |
| Model ready | 2 min 06 s |
| Initial cache reuse | ~0.31 GiB |

Three no-thinking inference runs measured **9.290**, **9.337**, and **9.408 tok/s**, for a median of **9.337 tok/s**.

This benchmark is the clearest v0.7.0-alpha demonstration of NIBIA's intended capacity-expansion behavior: a 17.28 GiB GGUF was executed across the aggregate safe capacity of three heterogeneous computers rather than depending on one machine alone.

## Persistent tensor-cache observations

NIBIA's worker-local RPC tensor cache materially reduces repeat network transfer. During the stabilization acceptance run of Qwen3-30B-A3B at context 8192:

```text
Remote tensor payload planned: 10.80 GiB
Relay traffic observed:         0.32 GiB
Estimated avoided transfer:    ~10.48 GiB
Model ready:                    1 min 28 s
```

The bytes avoided are substantial, but the small residual cache-miss / resolution path is currently much slower than the initial bulk-transfer path. That is a known **post-alpha performance optimization item**, not a correctness blocker.

## Cross-family observations

These results are useful compatibility observations, but they are **not an apples-to-apples model benchmark** because model architecture, quantization, context, prompt, and selected fabric differed.

| Model | Distributed plan | Context | Model ready | Observed generation throughput |
| --- | --- | ---: | ---: | ---: |
| GPT-OSS 20B MXFP4 | Mac + Windows | 8192 | 1 min 11 s | 12.04 tok/s (300 generated reasoning tokens) |
| Gemma 3 27B IT Q4_K_M | Mac + Windows | 4096 | 2 min 16 s | 2.54 tok/s (127 completion tokens) |

GPT-OSS also measured 4.79 GiB of transfer at 68.9 MiB/s during its distributed load. Gemma's run demonstrated compatibility with a large dense model family; its observed relay/network accounting included runtime overhead and should not be interpreted as tensor payload alone.

## How to reproduce a network comparison

For a meaningful network comparison, change only the network transport while keeping the rest of **your own test environment** as consistent as practical. Your hardware does not need to match NIBIA's reference lab.

Keep constant where possible:

- the Primary and Worker machines;
- NIBIA version and managed llama.cpp runtime;
- GGUF model and quantization;
- context size;
- prompt and generation settings;
- selected NIBIA nodes;
- power state;
- tensor-cache state;
- VPN state and local-LAN access policy.

For Wi-Fi-versus-Ethernet benchmarking, prefer testing with the **VPN disabled** unless the VPN itself is part of the experiment. If a VPN must remain enabled, keep the same VPN, route, and local-LAN access settings for every run. NIBIA itself does not require VPN software to be disabled when the VPN permits normal LAN reachability.

Record:

- selected nodes and planned shares;
- RTT / LAN latency;
- model-ready elapsed time;
- remote tensor payload planned;
- relay/network traffic observed;
- average transfer rate;
- prompt tokens/s;
- generation tokens/s.

For publishable throughput numbers, keep the model loaded and repeat the same short prompt at least three times per network mode, then report the individual runs and median. Also report cache state: cold-load and warm-cache load times are not directly comparable.

## Interpretation rules

- Do not claim that Ethernet always makes NIBIA faster; report the measured topology and workload.
- Do not describe independent node-local RAM as physical shared memory.
- Do not compare token/s from different model families as if architecture and quantization were equivalent.
- Adding more nodes primarily expands safe capacity; it does not automatically increase generation throughput.
- Network throughput matters most during distributed model loading and whenever inference requires enough cross-node communication for latency/bandwidth to become a bottleneck.
