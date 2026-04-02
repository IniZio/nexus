# Roadmap

## Current focus

Nexus roadmap is centered on remote workspace core quality and usability.

## Active tracks

1. Workspace daemon reliability
   - lifecycle robustness
   - compose-aware forwarding
   - readiness correctness

2. Workspace SDK parity
   - stable typed APIs
   - spotlight and readiness ergonomics
   - test coverage for core methods

3. Core docs quality
   - keep reference docs aligned with implemented behavior
   - remove drift from removed module surfaces

## Verification gates

- `packages/nexus`: `go test ./...`
- `packages/sdk/js`: `pnpm exec tsc --noEmit` and `pnpm exec jest --runInBand`
- root CI: core-only task pipeline
