# Contributing to NIBIA Fabric

NIBIA Fabric is currently CPU-first and release-focused. Contributions should preserve the simple prebuilt end-user path and avoid introducing heavy runtime/build dependencies unless there is a clear product need.

## Development requirements

- Go 1.23.x (the first Experimental Alpha release build uses Go 1.23.2).
- No local llama.cpp source build is required for ordinary NIBIA Fabric development unless working specifically on runtime packaging/fallback paths.

## Validate changes

```bash
go test ./...
go vet ./...
go test -race ./...
```

Build local binaries:

```bash
make build
```

Release maintainers can generate the exact public-named macOS/Linux/Windows ZIPs plus checksum manifest with:

```bash
make release-assets
```

That packaging path cross-builds with `-trimpath -buildvcs=false`, specializes the Unix installer for the target OS/architecture, and keeps the release payload minimal. End users do not need these build tools.

## Design rules

- Agents are persistent; compute workers are on-demand.
- Minimum Safe Fabric is the default scheduler policy.
- Do not describe independent node memory as physical shared RAM.
- Keep raw llama.cpp RPC loopback-only on Workers.
- Keep SERVE localhost-only by default in the current release line.
- Prefer pinned precompiled dependencies over asking end users to compile them.
- Keep Windows, macOS, and Linux behavior explicit and testable.
- Preserve protocol compatibility deliberately; protocol changes require explicit migration/re-pairing analysis.
- Treat pairing as a trusted-LAN bootstrap and keep post-pairing Agent/relay traffic authenticated.

## Pull requests

A change that affects lifecycle, scheduling, runtime packaging, security, wire protocol, or cross-platform behavior should include focused tests and a short acceptance note describing any physical validation required.

A change that adds, removes, or changes a public CLI command or flag should update [docs/CLI.md](docs/CLI.md) in the same pull request.

## Developer Certificate of Origin (DCO)

NIBIA Fabric uses the Developer Certificate of Origin (DCO) for contributions. A DCO sign-off records that you have the right to submit your contribution under the project's Apache-2.0 license.

Sign off each commit with:

```bash
git commit -s
```

This adds a line such as:

```text
Signed-off-by: Your Name <you@example.com>
```

Read the DCO 1.1 at https://developercertificate.org/.

NIBIA Fabric does not require contributors to assign copyright to the project as a condition of contribution at this stage. See [GOVERNANCE.md](GOVERNANCE.md) for the current project-maintenance model.
