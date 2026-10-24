# Agent Development Instructions

## Scope

Pulsar-Dock is a security-first desktop Agent collaboration workbench. Product behavior, architecture boundaries, and test evidence are documented under `docs/`.

## Required workflow

- Every code change must be made on a branch and delivered through a Pull Request.
- Every PR must reference an existing GitHub Issue and describe tests, risks, and known gaps.
- Before merging, an independent child Agent must read and follow the complete `deep-code-review` Skill and its required references against the latest PR head. Re-review fixes, require green CI, and merge only within the user's explicit project authorization. Release, tags, and deployment require separate authorization.
- Windows and macOS are the minimum desktop packaging targets. Record architecture, build results, signing/notarization limits, and actual installation/startup checks separately; Go-core CI is not desktop-package validation.
- Do not push directly to `main`.
- Keep commits small and meaningful. Preserve the real development history; do not batch the whole project into one final commit.
- Before the 2026-10-26 09:00 CST code freeze, keep the repository buildable and the working tree understandable.

## Security rules

- Never commit API keys, passwords, OAuth tokens, private keys, real model credentials, or personal configuration.
- Use synthetic credentials and fixtures for tests.
- Do not run an Agent directly on the host when the requested security mode requires an approved RuntimeBackend.
- Do not weaken isolation or network policy to make a demo pass.
- A task record is not a child environment. Do not report a child Agent as started until the RuntimeBackend created and inspected it.
- Changes to `RuntimeBackend`, policy evaluation, credential handling, MCP gateway authorization, or artifact application require focused tests.

## Code conventions

- Go code uses `gofmt`, `go test ./...`, and `go vet ./...`.
- Keep Controller code independent from sbx/Docker/WSL details; implement those behind a RuntimeBackend.
- Keep MCP tool handlers narrow. Do not expose Controller administration APIs to Agent environments.
- Keep snapshots and artifact references immutable. Applying a result to the user's project requires an explicit user action and a baseline check.

## Tests and evidence

- Unit tests cover policy, task state, idempotency, and MCP handlers.
- Integration/security tests use synthetic projects, virtual credentials, and controlled endpoints.
- Keep reusable test code in `tests/` and concise conclusions in `docs/`. The root `validation/` directory is ignored local-only evidence: do not commit raw logs, machine state, installation scripts, or generated manifests from it.
- A failed or unverified check must remain visible in the PR and validation docs.
- Do not call the project production-ready based on a single local run.
