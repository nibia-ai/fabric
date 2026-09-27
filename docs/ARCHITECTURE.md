# NIBIA Fabric v0.7 N-node architecture

```text
                   NIBIA Controller / Coordinator host

      llama-cli --rpc 127.0.0.1:55052,127.0.0.1:55053,...
                         |
                         +-- local runtime device (for example MTL0)
                         |
                         +-- RPC0 -> loopback relay 55052
                         |             |
                         |          mTLS reverse tunnel
                         |             |
                         |       Remote Agent A
                         |             |
                         |       127.0.0.1:50052
                         |       ggml-rpc-server
                         |
                         +-- RPC1 -> loopback relay 55053
                                       |
                                    mTLS reverse tunnel
                                       |
                                 Remote Agent B
                                       |
                                 127.0.0.1:50052
                                 ggml-rpc-server
```

Raw llama.cpp RPC stays loopback-only on every remote node. NIBIA's Agent initiates authenticated reverse tunnels to the Controller, so adding Windows does not require exposing `ggml-rpc-server` on the LAN.

The **Primary Node is a role, not a privileged operating system**. Any supported desktop platform can host Controller + Coordinator + Agent + CLI + local compute. Local backend details differ by platform (for example Apple Silicon Metal versus CPU execution on Linux/Windows), but the Fabric role and wire protocol are the same.

## Planner

The executor discovers concrete llama.cpp devices first, subtracts a configurable reserve from each device, keeps the coordinator's local device, then sorts remote candidates by usable memory and adds the minimum sufficient subset for the requested model. The tensor split is computed dynamically from selected usable memory.

This is a capacity-first policy, not yet a throughput optimizer. Later scheduling can incorporate measured network throughput, CPU characteristics, inference throughput and historical pressure.

## Inventory

Every Agent automatically reports slow-changing node identity/inventory plus dynamic heartbeat telemetry:

- OS and architecture;
- CPU model and physical/logical cores;
- RAM total/available/used;
- CPU use and memory pressure where supported;
- network interfaces, virtual/VPN classification and preferred LAN address;
- runtime inventory and versions.

## Telemetry semantics

Per-node execution summaries currently use two different measurements:

- `planned share`: scheduler/model partition percentage;
- scheduler health: system-wide CPU/RAM remains heartbeat-sampled;
- inference accounting: local `llama-cli` and managed remote `ggml-rpc-server` CPU/RSS are process-scoped and measured over the execution window.

They must not be conflated with process-only utilization.

## Memory semantics

`aggregate node-local capacity` means multiple independent memory domains made usable by runtime-level model partitioning. It does not mean SMP, NUMA-style shared physical memory, or a shared address space.


## SERVE recovery

A distributed SERVE workload treats loss of a selected worker as a fail-stop event for the active request. The service process can remain alive in a bounded recovery state, wait for the selected capacity to return, rebuild the RPC fabric, revalidate runtime/device readiness, replan memory safety, reload the model, and restore the local API. This is service recovery, not transparent continuation of the interrupted generation.
