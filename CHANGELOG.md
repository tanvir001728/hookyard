# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Project documentation, community guidelines and contribution templates.
- `hookyard serve` with `/healthz` and `/readyz` endpoints, structured logging and graceful shutdown.
- Postgres storage with embedded, lock-protected migrations; `hookyard migrate [up|status]`; `HOOKYARD_DATABASE_URL` and `HOOKYARD_AUTO_MIGRATE` settings; `/readyz` checks the database.
- OpenAPI 3.1 specification for the v1 API (`api/openapi.yaml`), linted in CI.
