# NIBIA Fabric security model

NIBIA Fabric v0.7.0-alpha is designed for a **trusted local network** and uses conservative local-only defaults for user-facing inference.

## Trust boundary

The Experimental Alpha assumes that participating machines and the local network are trusted. It is not intended to turn a hostile or public LAN into a safe execution environment.

Do not expose Controller pairing, secure-node, relay, or SERVE ports directly to the public Internet.

## Pairing bootstrap

Initial pairing occurs before the joining Agent has an mTLS identity.

- Pairing-code creation is restricted to localhost on the Controller.
- Pairing codes are cryptographically generated six-digit temporary credentials.
- A code expires after 5 minutes and is exhausted after a small bounded number of failed claims.
- The claim endpoint is therefore intended only for the same trusted LAN.
- After a successful claim, the Controller provisions the Agent identity and the code is invalidated.

Treat a pairing code like a temporary credential. Do not pair across an untrusted or hostile network.

## Control plane

- Agent-to-Controller secure communication uses **TLS 1.3 mTLS** after one-time pairing.
- Controller administrative operations are restricted to localhost by design.
- Version and NIBIA wire-protocol compatibility are checked across CLI, Controller, and Agents.
- Agent trust records support revocation.
- Persistent controller/agent identity state uses user-local state directories; private keys are written with restrictive file permissions on Unix-like systems.

## Data plane

- `ggml-rpc-server` is managed on selected Workers only.
- Raw llama.cpp RPC listens on Worker loopback, not directly on the LAN.
- NIBIA carries RPC over authenticated reverse relay tunnels to the Controller/Primary.
- Controller-side relay bridge endpoints are loopback-only.
- Worker activation is on-demand; persistent Agents do not imply persistent compute workers.

## SERVE

- llama-server binds to `127.0.0.1` by default.
- Non-loopback SERVE binding is rejected in this Experimental Alpha.
- The first public alpha therefore does not expose an unauthenticated LAN inference endpoint.
- The local Web UI and OpenAI-compatible API are available only on the Primary unless the user deliberately adds a separate trusted frontend/proxy.

If you add a proxy, remote frontend, SSH tunnel, or other exposure layer, its authentication, TLS, CORS, and access-control policy become part of your security boundary.

## Managed runtime integrity

NIBIA's managed llama.cpp runtime is pinned to a specific upstream release and commit. The downloaded platform archive is checked against a hard-coded SHA-256 before extraction and execution, and the extracted runtime is health/identity checked.

This protection does **not** extend to arbitrary GGUF model files. NIBIA does not currently provide model signing or model-file provenance verification. Use models from sources you trust.

## Power Guard

Power Guard is active only for selected workloads. NIBIA does not permanently rewrite system sleep policy. Current backends are macOS `caffeinate`, Linux `systemd-inhibit`, and Windows `SetThreadExecutionState`.

Power Guard cannot guarantee prevention of critical-battery shutdown/sleep, lid-close behavior, manual sleep, firmware/Modern Standby, or administrator-enforced policy.

## Memory policy

The default SAFE policy reserves memory on every selected node. This reduces host-stability risk but does not eliminate every OOM or runtime-failure scenario.

An explicit expert max-capacity/unsafe policy is intentionally not exposed in the first public alpha.

## Threat-model limits

Outside the current NIBIA trust boundary:

- physical host compromise;
- malicious local administrators;
- hostile/untrusted LANs;
- operating-system compromise;
- arbitrary model-file safety;
- third-party frontend/proxy security;
- denial of service by other processes exhausting host resources;
- public-Internet service exposure.

## Reporting vulnerabilities

Use GitHub Private Vulnerability Reporting when it is enabled for the public repository. Do not publish sensitive exploit details in a public issue.
