---
name: pre-push-checks
description: Run the full CI check suite locally before any git push — go build, unit tests, golangci-lint, buf proto checks, govulncheck, image digest pinning, integration tests. Use when about to push, ship, or open or update a PR in this repo, so CI is already green before it runs.
---

# Pre-Push Checks

Never push red code. Before every `git push` in this repo, run every locally-runnable CI check, fix all failures, and re-run until the whole suite is green. A push should confirm green CI, not discover red CI.

## Suite (run from the repo root, in this order)

| # | Check | Command | CI job it mirrors |
|---|-------|---------|-------------------|
| 1 | Build | `go build ./...` | build |
| 2 | Unit tests | `make test-short` | test-unit |
| 3 | Lint | `make lint` | golangci-lint |
| 4 | Proto lint + format | `make proto-lint` | buf-lint |
| 5 | Proto breaking | `buf breaking --against ".git#branch=main"` | buf-lint (breaking) |
| 6 | Vulnerabilities | `govulncheck ./...` | govulncheck |
| 7 | Image digest pinning | `make check-image-digests` | lint workflow step |
| 8 | Integration tests | `make test-integration` | test-integration |

Rules:

- Checks 1-7 are mandatory before every push. Run 8 as well when integration-relevant code changed (`internal/`, `pkg/`, `migrations/`, `integration/`) and Docker is available; if skipped, say so and why — never silently.
- Iterate scoped for speed, then finish with the full suite: `go test -race -run TestName ./internal/<pkg>/`, `golangci-lint run internal/<pkg>/...`.
- `make lint` must end with `0 issues.` — findings are blocking. Fix the code; never weaken `.golangci.yml` and never add `//nolint` without an explanation of why the finding is a false positive.
- If `buf breaking` fails because local `main` is stale, run `git fetch origin main` first, then re-run against `.git#branch=origin/main`.
- If `govulncheck` is not on PATH, run `go run golang.org/x/vuln/cmd/govulncheck@latest ./...`. Report any finding whose fix is a dependency bump rather than silently pinning around it.

## Fix discipline

Fix the root cause. After each fix re-run the failed check; after the last fix re-run the entire suite — a late fix can break an earlier check.

## Before pushing

1. Whole suite green, then commit with a conventional message (`feat:`, `fix:`, `chore:`, `docs:`, `refactor:`, `test:`) and a `Co-authored-by:` trailer for the current model using `<noreply@opencode.ai>` as the email.
2. Push, then verify CI started: `gh pr checks <number>` or `gh run list --branch <branch> --limit 3`. CodeQL has no local equivalent — expect it to run remotely only.
3. If any check could not run locally, state exactly which and why. Do not claim CI will be green on partial evidence.