# Contributing to Hookyard

Thanks for your interest in Hookyard! This guide explains how to propose changes and what we expect
from a pull request.

## Ways to contribute

- **Report a bug:** open a [bug report](https://github.com/tanvir001728/hookyard/issues/new?template=bug_report.yml).
- **Suggest a feature:** open a [feature request](https://github.com/tanvir001728/hookyard/issues/new?template=feature_request.yml).
- **Ask a question:** use [GitHub Discussions](https://github.com/tanvir001728/hookyard/discussions).
- **Send code or docs:** look for issues labeled
  [`good first issue`](https://github.com/tanvir001728/hookyard/labels/good%20first%20issue) or
  [`help wanted`](https://github.com/tanvir001728/hookyard/labels/help%20wanted).

For anything larger than a small fix, please **open or comment on an issue first** so we can agree on
the approach before you invest time in it.

## Repository layout

| Path | What lives there |
| --- | --- |
| `cmd/` | Entry points (`hookyard` server, `flakyvendor` test vendor) |
| `internal/` | Go packages for the server (queue, workers, API, policies, …) |
| `api/` | OpenAPI specification, the source of truth for the HTTP API |
| `web/` | Dashboard (React + TypeScript), embedded into the server binary |
| `sdk/typescript/` | `@hookyard/sdk`, the official TypeScript SDK |
| `deploy/` | Docker Compose and deployment examples |
| `docs/` | Documentation |

## Development setup

Prerequisites: **Go 1.25+**, **Node.js 22+** with **pnpm**, and **Docker**.

```sh
git clone git@github.com:tanvir001728/hookyard.git
cd hookyard
```

Build, test and local-run instructions will be added here as soon as the first code lands.

## Pull request process

1. Fork the repository and create a branch from `main`:
   `feat/<short-name>`, `fix/<short-name>`, `docs/<short-name>` or `chore/<short-name>`.
2. Make your change, with tests. Bug fixes should include a test that fails without the fix.
3. Run the linters and tests locally (`make lint test`).
4. Open a pull request, fill in the template, and link the issue it resolves (`Closes #123`).
5. A maintainer will review it. Once CI is green and the PR is approved, it is **squash-merged**, so the
   PR title becomes the commit message on `main`.

## Commit messages and PR titles

We follow [Conventional Commits](https://www.conventionalcommits.org/). Because PRs are squash-merged,
**the PR title is what matters most**:

```
<type>(<optional scope>): <short summary in the imperative mood>
```

| Type | Use for |
| --- | --- |
| `feat` | A new user-facing feature |
| `fix` | A bug fix |
| `docs` | Documentation only |
| `refactor` | Code change that neither fixes a bug nor adds a feature |
| `perf` | Performance improvement |
| `test` | Adding or fixing tests |
| `build` / `ci` | Build system, dependencies, CI configuration |
| `chore` | Maintenance that doesn't fit the above |

Examples: `feat(queue): add exponential backoff with full jitter`,
`fix(api): reject requests for unknown upstreams with 422`.

Mark breaking changes with `!` (for example `feat(api)!: rename retry_policy to retry`) and explain the
migration in the PR description.

## Coding guidelines

- **Go:** code must pass `gofmt`, `go vet` and `golangci-lint`. Prefer small packages with clear
  responsibilities, return errors instead of panicking, and pass `context.Context` through I/O paths.
- **TypeScript:** strict mode, no `any` in public APIs, formatted with the repository's formatter config.
- **API changes** start in `api/openapi.yaml`, so the server, SDK and docs stay in sync.
- **User-facing changes** need an entry under `Unreleased` in [CHANGELOG.md](CHANGELOG.md).

## Code of conduct

This project follows our [Code of Conduct](CODE_OF_CONDUCT.md). By participating, you agree to uphold it.

## License

By contributing, you agree that your contributions will be licensed under the [MIT License](LICENSE).
