# Dependencies

> **Release architecture scope:** v0.7.0-alpha provides 64-bit binaries only. 32-bit operating systems and CPU architectures are not supported.

NIBIA Fabric's public install path is intentionally prebuilt-first. End users should not need Go, Python, CMake, Visual Studio Build Tools, Homebrew, or a local llama.cpp source build.

## End-user requirements

### macOS arm64

- Apple Silicon Mac running a supported 64-bit macOS release.
- Built-in `/usr/bin/caffeinate` and `/usr/bin/pmset` for Power Guard.
- Network access for first-time managed-runtime setup and for downloading a model from the user's chosen registry/source.

### Linux amd64

- 64-bit Linux on amd64/x86-64.
- `systemd-inhibit` for workload-scoped Power Guard on the current release path. It is normally present on systemd-based distributions.
- Network access for first-time managed-runtime setup and model download.

### Windows amd64

- 64-bit Windows on amd64/x64.
- PowerShell for the packaged installer.
- No Python, Go, CMake, or Visual Studio installation is required.
- Power Guard uses the native Windows `SetThreadExecutionState` API.
- Network access for first-time managed-runtime setup and model download.

## Models

Download the GGUF model directly to the Primary from its model registry/source. Workers do not require a copy of the model file. Copying GGUF files between lab computers is a development shortcut, not the normal user workflow.

## Network

NIBIA Fabric requires IP reachability between nodes on the trusted LAN. Wi-Fi is supported. Ethernet is recommended for distributed workloads when practical. VPN/firewall software must allow local-LAN communication.

## Development/build requirements

Only contributors building NIBIA from source need Go. The current codebase is validated with Go 1.23.x. CMake/toolchains are not part of the normal NIBIA build because the public runtime path uses pinned precompiled llama.cpp artifacts. Building llama.cpp from source remains a development/fallback workflow only.

## Managed runtime and optional clients

### llama.cpp runtime

NIBIA Fabric v0.7.0-alpha uses [llama.cpp](https://github.com/ggml-org/llama.cpp) as its managed inference/runtime backend. NIBIA Fabric pins the public runtime used by this release to build `b10902`, commit `df03399`, downloads the expected precompiled artifact over HTTPS, and verifies its SHA-256 before execution.

Normal NIBIA Fabric users do not need to clone or build llama.cpp from source. llama.cpp remains an independent **MIT-licensed** upstream project and retains its own license and notices.

### Optional validated clients

The following projects are **not NIBIA Fabric dependencies** and are not required for installation or serving:

- [Open WebUI](https://github.com/open-webui/open-webui) — separately licensed optional external UI, physically validated against NIBIA Fabric's OpenAI-compatible API in the release test mode. It is not bundled with NIBIA; consult the upstream `LICENSE` and license history for the version you use.
- [OpenClaw](https://github.com/openclaw/openclaw) — MIT-licensed optional external client/agent integration, physically validated with NIBIA Fabric configured as an OpenAI-compatible local provider in the release test mode. It is not bundled with NIBIA.

Their independent installation, licensing, features, and security behavior are governed by their respective projects.
