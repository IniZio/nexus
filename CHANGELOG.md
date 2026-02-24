# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.1.1] - 2026-02-23

### Added
- Comprehensive test coverage (285+ tests)
- Performance optimizations (41% binary size reduction)
- Security fixes (path traversal prevention)

### Fixed
- CI test failures
- Documentation improvements
- Code quality improvements

## [1.0.0] - 2026-02-24

### Breaking Changes

- **Removed `--template` flag** from `nexus workspace create`. Users now provide Dockerfile directly, giving full control over their environment.

### Added

- **Comprehensive Examples Section** with 6 complete examples:
  - [Quickstart](docs/examples/quickstart/) - 5-minute getting started guide
  - [Node + React](docs/examples/node-react/) - Modern frontend development with HMR
  - [Python + Django](docs/examples/python-django/) - Python web applications with PostgreSQL
  - [Go Microservices](docs/examples/go-microservices/) - Multi-service architecture
  - [Fullstack + PostgreSQL](docs/examples/fullstack-postgres/) - Three-tier application
  - [Remote Server](docs/examples/remote-server/) - Cloud development environments
- Each example includes README, Dockerfile, docker-compose.yml (where applicable), and demo.sh

### Changed

- **Simplified CLI** by removing template system - users now provide their own Dockerfile
- **Documentation restructured** with examples focus and TanStack Router-style examples section
- Updated navigation in MkDocs to include examples

## [0.2.0] - 2026-02-18

### Added

- Documentation system with Diátaxis structure
- A/B testing for doc templates
- Usage telemetry (local-first)
- GitHub Pages integration
- Skeptical reviewer system

### Fixed

- SQL NULL handling in database queries
- Workspace creation reliability
- Doc command registration

### Changed

- Improved CLI command organization
- Enhanced telemetry collection privacy

## [0.1.0] - 2026-02-15

### Added

- MVP: Container workspaces with git worktrees
- Task coordination with verification
- Multi-service templates
- Ralph loop (auto skill improvement)
- Basic CLI interface

[0.1.1]: https://github.com/your-org/nexus/compare/v0.1.0...v0.1.1
[0.2.0]: https://github.com/your-org/nexus/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/your-org/nexus/compare/v0.1.0
