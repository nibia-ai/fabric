# Security Policy

NIBIA Fabric is currently an **Experimental Alpha** intended for trusted local networks.

## Reporting a vulnerability

Please do not disclose suspected security vulnerabilities in a public issue.

For the public GitHub repository, use **GitHub Private Vulnerability Reporting** from the repository's **Security** tab once it is enabled. If private vulnerability reporting is not yet available, do not publish sensitive details publicly; contact the repository owner through a private channel and wait for a private reporting path to be provided.

## Supported release

Security fixes during the Experimental Alpha phase target the latest published `v0.7.0-alpha` release line only.

## Security model

Key release defaults:

- trusted-LAN deployment model;
- short-lived one-time pairing bootstrap;
- TLS 1.3 mTLS for Agent control traffic after pairing;
- authenticated mTLS reverse RPC relay;
- raw llama.cpp RPC kept on worker loopback;
- localhost-only SERVE/API by default;
- localhost-only Controller administrative operations;
- pinned, SHA-256-verified managed llama.cpp runtime downloads.

Model files and third-party frontends remain outside NIBIA's trust boundary. Use GGUF files and client software from sources you trust.

For architecture, pairing, threat-boundary, and Power Guard details, see [docs/SECURITY.md](docs/SECURITY.md).
