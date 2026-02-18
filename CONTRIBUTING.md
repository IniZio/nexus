# Contributing to Nexus

Thank you for your interest in contributing to Nexus! This document provides guidelines and instructions for contributing.

## Getting Started

### Prerequisites

- Go 1.21+
- Docker
- Git
- Make (optional)

### Setting Up Development Environment

1. Fork the repository on GitHub
2. Clone your fork locally:

```bash
git clone https://github.com/YOUR_USERNAME/nexus.git
cd nexus
```

3. Install dependencies:

```bash
go mod download
make setup
```

4. Create a feature branch:

```bash
git checkout -b feature/your-feature-name
```

## Development Workflow

### Code Style

- Follow Go conventions: https://go.dev/doc/effective_go
- Run `go fmt` before committing
- Use `go vet` and `staticcheck` to catch issues
- Maximum line length: 120 characters

### Testing Requirements

All contributions must include appropriate tests:

```bash
# Run unit tests
go test ./... -short

# Run integration tests
go test ./... -tags=integration

# Run all tests with coverage
go test ./... -coverprofile=coverage.html
go cover -html=coverage.html
```

- Unit tests are required for new functions/methods
- Integration tests required for new features
- Maintain or improve code coverage

### Commit Messages

Follow the [Conventional Commits](https://www.conventionalcommits.org/) format:

```
<type>[optional scope]: <description>

[optional body]

[optional footer(s)]
```

Types:
- `feat`: New feature
- `fix`: Bug fix
- `docs`: Documentation only
- `style`: Formatting, no code change
- `refactor`: Code restructuring
- `test`: Adding/missing tests
- `chore`: Maintenance

Examples:

```
feat(telemetry): add local-first analytics collection
fix(database): handle SQL NULL values in queries
docs(readme): update installation instructions
```

### Pull Request Process

1. **Before submitting:**
   - Ensure all tests pass
   - Update documentation as needed
   - Add entry to CHANGELOG.md
   - Squash commits into logical units

2. **PR Requirements:**
   - Clear title and description
   - Link to related issues
   - At least one approval required
   - All CI checks must pass

3. **Review Process:**
   - Address all feedback
   - Keep PR focused and small
   - Force push only for reviewer requests

### Branch Strategy

- `main`: Stable release branch
- `develop`: Development branch (if using GitFlow)
- `feature/*`: New features
- `fix/*`: Bug fixes
- `release/*`: Release preparation

## Architecture

See [docs/architecture](/docs/architecture) for system design documentation.

## Questions?

- Open an issue for bugs
- Start a discussion for feature ideas
- Join our community chat

## Code of Conduct

Be respectful and inclusive. See [CODE_OF_CONDUCT.md](./CODE_OF_CONDUCT.md).
