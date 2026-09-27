# NIBIA Fabric Quickstart

This is the shortest current path from a release download to a working trusted-LAN NIBIA Fabric.

## Before you start

> **Architecture requirement:** NIBIA Fabric v0.7.0-alpha release binaries require a supported 64-bit platform: macOS arm64 (Apple Silicon), Linux amd64 (x86-64), or Windows amd64 (x64). Any of these supported platforms can be the **Primary Node**. **32-bit operating systems and CPU architectures are not supported.**

- Put the participating computers on the same reachable trusted LAN.
- Ethernet is recommended for larger distributed models when practical; Wi-Fi is supported.
- VPN software can block or reroute private-LAN traffic. For first setup, disable the VPN or ensure local-LAN access is allowed.
- Keep laptops on external power when practical and configure the OS to avoid automatic sleep during long workloads.
- Download the correct NIBIA release archive for each platform and the matching checksum file.

## 1. Verify the release archive

Download `NIBIA_v0.7.0-alpha_SHA256SUMS.txt` from the same GitHub Release as the platform ZIP. Verify the archive **before extracting or running it**.

### macOS

```bash
shasum -a 256 nibia-v0.7.0-alpha-macos-arm64.zip
grep 'nibia-v0.7.0-alpha-macos-arm64.zip' NIBIA_v0.7.0-alpha_SHA256SUMS.txt
```

### Linux

```bash
sha256sum nibia-v0.7.0-alpha-linux-amd64.zip
grep 'nibia-v0.7.0-alpha-linux-amd64.zip' NIBIA_v0.7.0-alpha_SHA256SUMS.txt
```

### Windows PowerShell

```powershell
Get-FileHash .\nibia-v0.7.0-alpha-windows-amd64.zip -Algorithm SHA256
Select-String 'nibia-v0.7.0-alpha-windows-amd64.zip' .\NIBIA_v0.7.0-alpha_SHA256SUMS.txt
```

The computed SHA-256 must match the entry in the checksum file for your archive.

## 2. Install NIBIA on every node

### macOS / Linux

Extract the archive, open a terminal inside the extracted directory, and run:

```bash
./install.sh
```

The installer places `nibia`, `nibia-agent`, and `nibia-controller` in `~/.local/bin`, persists that directory in the shell profile when needed, bootstraps the pinned llama.cpp runtime, and runs a local doctor check.

Open a new terminal if the installer says the PATH was updated. Then verify:

```bash
nibia version
nibia doctor --local
```

### macOS Experimental Alpha signing note

The first Experimental Alpha archive may be distributed without Apple Developer ID notarization. Verify the archive SHA-256 before running it. The macOS installer checks whether quarantine is still attached to the NIBIA binaries and, if so, stops **before copying or executing them** with checksum-first guidance. After the ZIP checksum matches the published release checksum, remove quarantine from the verified extracted directory and rerun the installer:

```bash
xattr -dr com.apple.quarantine .
./install.sh
```

NIBIA does not remove quarantine automatically. This note is specific to the unsigned Experimental Alpha and should be removed once release archives are signed and notarized.

### Windows

Extract the archive, open PowerShell in that directory, and run:

```powershell
powershell -ExecutionPolicy Bypass -File .\install.ps1
```

The installer uses `%USERPROFILE%\.nibia\bin`, updates the user PATH, bootstraps the pinned runtime, and runs a local doctor check.

Open a new PowerShell if needed. Then verify:

```powershell
nibia version
nibia doctor --local
```

For this release, `nibia version` should report `v0.7.0-alpha` and protocol `2`.

## 3. Start the Primary Controller

Choose one supported machine as the **Primary Node**. macOS arm64, Linux amd64, and Windows amd64 can all fill this role. Use that machine's LAN address for `--advertise-host`; this keeps the secure Controller URL unambiguous even when the Primary also has Wi-Fi, VPN, or other interfaces.

macOS/Linux:

```bash
nibia-controller \
  --pair-listen 0.0.0.0:8080 \
  --secure-listen 0.0.0.0:8443 \
  --relay-listen 0.0.0.0:9443 \
  --advertise-host <PRIMARY_LAN_IP>
```

Windows PowerShell:

```powershell
nibia-controller `
  --pair-listen 0.0.0.0:8080 `
  --secure-listen 0.0.0.0:8443 `
  --relay-listen 0.0.0.0:9443 `
  --advertise-host <PRIMARY_LAN_IP>
```

Leave the Controller running. The pairing port is a trusted-LAN bootstrap endpoint; secure Agent control and reverse relay traffic use mTLS after pairing.

NIBIA does not automatically create broad host-firewall exceptions. If the Primary host firewall blocks inbound LAN traffic (commonly on Windows), allow the configured Controller ports only on the intended trusted LAN/interface/profile. The Quickstart defaults are TCP `8080`, `8443`, and `9443`; do not expose them to the public Internet.

## 4. Pair the Primary Agent once

Open another terminal on the Primary and create a pairing code:

```bash
nibia pair --controller http://127.0.0.1:8080
```

Use the returned code immediately:

```bash
nibia-agent --pair http://127.0.0.1:8080 --code <PAIR_CODE> --name <PRIMARY_NAME>
```

Pairing is persisted. On later starts, run:

```bash
nibia-agent
```

## 5. Add Worker Nodes

On the Primary, create another pairing code:

```bash
nibia pair --controller http://127.0.0.1:8080
```

On a Worker:

```bash
nibia-agent --pair http://<PRIMARY_LAN_IP>:8080 --code <PAIR_CODE> --name <WORKER_NAME>
```

On later starts, run:

```bash
nibia-agent
```

Repeat for additional workers.

> **Multi-interface nodes:** if an Agent has more than one usable interface (for example Wi-Fi + Ethernet) and automatic selection chooses the wrong LAN address, add `--preferred-address <NODE_LAN_IP>` whenever starting that Agent, including its pairing start. This override is runtime-only in this Experimental Alpha. VPN/tunnel addresses are not valid preferred Fabric addresses.

## 6. Validate the fabric

On the Primary:

```bash
nibia doctor --controller http://127.0.0.1:8080
nibia nodes --controller http://127.0.0.1:8080
```

`nibia nodes` shows both current OS-available RAM and NIBIA `SAFE USABLE` capacity. Wait for participating Agents to show a fresh `READY` state before starting a large distributed workload.

## 7. Download a GGUF model to the Primary

Download the model directly to the **Primary Node** from a registry/source you trust. Workers do not need their own copy of the GGUF.

Example using the official **Qwen3-4B Q4_K_M** GGUF, one of the models physically validated with this release:

Model source: [Qwen/Qwen3-4B-GGUF](https://huggingface.co/Qwen/Qwen3-4B-GGUF)

### macOS / Linux

```bash
mkdir -p ~/Models
curl -L --fail \
  -o ~/Models/Qwen3-4B-Q4_K_M.gguf \
  'https://huggingface.co/Qwen/Qwen3-4B-GGUF/resolve/main/Qwen3-4B-Q4_K_M.gguf?download=true'
```

### Windows PowerShell

```powershell
New-Item -ItemType Directory -Force "$HOME\Models" | Out-Null
Invoke-WebRequest `
  -Uri "https://huggingface.co/Qwen/Qwen3-4B-GGUF/resolve/main/Qwen3-4B-Q4_K_M.gguf?download=true" `
  -OutFile "$HOME\Models\Qwen3-4B-Q4_K_M.gguf"
```

NIBIA Fabric verifies its managed llama.cpp runtime artifacts, but it does not currently sign or verify arbitrary model files.

## 8. Serve the model

Generic command:

### macOS / Linux

```bash
nibia serve \
  --model <path-to-model.gguf> \
  --ctx 4096 \
  --port 8081 \
  --controller http://127.0.0.1:8080
```

### Windows PowerShell

```powershell
nibia serve `
  --model <path-to-model.gguf> `
  --ctx 4096 `
  --port 8081 `
  --controller http://127.0.0.1:8080
```

For the Qwen3-4B example downloaded above:

### macOS / Linux

```bash
nibia serve \
  --model ~/Models/Qwen3-4B-Q4_K_M.gguf \
  --ctx 4096 \
  --port 8081 \
  --controller http://127.0.0.1:8080
```

### Windows PowerShell

```powershell
nibia serve `
  --model "$HOME\Models\Qwen3-4B-Q4_K_M.gguf" `
  --ctx 4096 `
  --port 8081 `
  --controller http://127.0.0.1:8080
```

NIBIA Fabric's default policy is **SAFE** with **Minimum Safe Fabric**. It does not activate every Worker merely because the Worker is connected.

`--ctx` is intentionally explicit in these examples because context requirements vary by model and workload. Choose a context supported by the model and suitable for the available SAFE capacity. `--port` and `--controller` are also shown explicitly so the serving endpoint and target Fabric are unambiguous.

### `run` vs `serve`

Use `serve` when you want the model to remain loaded for the built-in Web UI, the OpenAI-compatible API, or repeated requests. Stop it with `Ctrl+C`.

Use `run` for a single terminal prompt, scripting, or benchmark-style one-shot inference:

```bash
nibia run \
  --model <path-to-model.gguf> \
  --prompt "Reply exactly: NIBIA WORKS" \
  --tokens 64 \
  --ctx 4096 \
  --controller http://127.0.0.1:8080
```

`run` cleans up its transient RPC workers/relays and exits after the response. With `--session`, those resources remain available for the interactive session and are cleaned up when the session ends.

## 9. Open the built-in UI or API

Built-in/default **llama.cpp Web UI**:

```text
http://127.0.0.1:8081/
```

Local OpenAI-compatible API base:

```text
http://127.0.0.1:8081/v1
```

Optional independent clients physically validated in documented release test modes include:

- [Open WebUI](https://github.com/open-webui/open-webui)
- [OpenClaw](https://github.com/openclaw/openclaw)

Neither is required to use NIBIA Fabric. See [RELEASE_NOTES.md](RELEASE_NOTES.md) for the tested model/context/client combinations.

Stop SERVE with `Ctrl+C`. Persistent Agents can remain running for the next workload.

## Networking notes

- VPN software can route or block private-LAN traffic. If Workers become unreachable, disconnect the VPN or enable its local-network/LAN-access setting before changing NIBIA configuration.
- Ethernet is recommended when practical for distributed workloads, especially larger models, but it is not required.
- NIBIA Fabric's scheduler optimizes safe capacity first; adding more Workers does not automatically mean higher tokens/s.
- Raw llama.cpp RPC stays on Worker loopback and is bridged through the authenticated NIBIA relay.
- The measured Wi-Fi-versus-Gigabit-Ethernet A/B and the three-node wired reference setup are documented in [docs/BENCHMARKING.md](docs/BENCHMARKING.md).

## Recovery and power notes

If a selected remote Agent disappears during distributed inference, the active request cannot safely continue and is interrupted. SERVE can remain alive in a bounded recovery state, wait for selected capacity to return, rebuild the runtime, reload the model, and restore the API when possible. This is service recovery, not request replay or KV-cache replication; the interrupted request must be sent again by the client.

For long distributed workloads, keep participating laptops on external power when practical and configure the operating system to avoid automatic sleep. NIBIA Fabric activates a workload-scoped Power Guard during selected workloads, but critical-battery behavior, lid-close policy, manual sleep, firmware/Modern Standby, or administrator policy can still suspend a node.

## Uninstall

The release archives include an uninstall helper. By default it removes the NIBIA binaries and installer-added PATH entry while preserving `~/.nibia` / `%USERPROFILE%\.nibia` state, pairings, and the managed runtime so a later reinstall can reuse them.

### macOS / Linux

From the extracted release directory:

```bash
./uninstall.sh
```

To also remove NIBIA state, pairings, and the managed runtime:

```bash
./uninstall.sh --purge-state
```

### Windows PowerShell

```powershell
.\uninstall.ps1
```

To also remove NIBIA state, pairings, and the managed runtime:

```powershell
.\uninstall.ps1 -PurgeState
```

> `--purge-state` / `-PurgeState` is destructive. A purged node must be paired again.

## Where to go next

- Complete CLI reference: [docs/CLI.md](docs/CLI.md)
- Validation matrix, tested contexts, and client modes: [RELEASE_NOTES.md](RELEASE_NOTES.md)
- Measured performance and Wi-Fi/Ethernet methodology: [docs/BENCHMARKING.md](docs/BENCHMARKING.md)
- Architecture: [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)
- Security model: [docs/SECURITY.md](docs/SECURITY.md)
- Troubleshooting: [docs/TROUBLESHOOTING.md](docs/TROUBLESHOOTING.md)
