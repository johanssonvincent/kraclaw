# Contributing to Kraclaw

Thank you for your interest in contributing to Kraclaw! This guide will help you get started.

## Development Setup

### Prerequisites

- Go 1.26.1+
- Docker (for integration tests and container builds)
- Redis 5.0+ (for local development)
- MySQL 5.7+ or 8.0 (for local development)
- [buf](https://buf.build/) (for protobuf code generation)
- [golangci-lint](https://golangci-lint.run/) (for linting)

### Building

```bash
make build          # Build server binary
make build-tui      # Build TUI client
```

### Testing

```bash
make test           # Run all tests (requires Docker for integration tests)
make test-short     # Unit tests only (no Docker required)
make lint           # Run golangci-lint
make fmt            # Format code
```

### Running Locally

See the [Quick Start](README.md#-quick-start) section in the README.

## Making Changes

### Branch Workflow

1. Fork the repository
2. Create a feature branch from `main`: `git checkout -b feat/my-feature main`
3. Make your changes following the conventions below
4. Push to your fork and open a pull request against `main`

### Commit Messages

We use [Conventional Commits](https://www.conventionalcommits.org/):

- `feat:` — New feature
- `fix:` — Bug fix
- `docs:` — Documentation only
- `chore:` — Maintenance (deps, CI, tooling)
- `refactor:` — Code change that neither fixes a bug nor adds a feature
- `test:` — Adding or updating tests

### Code Conventions

- **Formatting:** Run `make fmt` before committing
- **Linting:** All code must pass `make lint`
- **Testing:** Table-driven tests, meaningful test names following `Test<Function>_<Scenario>`
- **Logging:** Use `log/slog` only — no third-party loggers
- **Errors:** Wrap with `fmt.Errorf("<operation>: %w", err)`

### Pull Request Checklist

- [ ] Code compiles (`make build`)
- [ ] Tests pass (`make test-short` at minimum)
- [ ] Linter passes (`make lint`)
- [ ] Commit messages follow conventional commits
- [ ] New features include tests

## Reporting Issues

Use [GitHub Issues](https://github.com/johanssonvincent/kraclaw/issues) for bug reports and feature requests. Use the provided templates when available.

## Security Issues

Please see [SECURITY.md](SECURITY.md) for reporting security vulnerabilities. **Do not open public issues for security bugs.**

## License

By contributing, you agree that your contributions will be licensed under the [Apache License 2.0](LICENSE).
