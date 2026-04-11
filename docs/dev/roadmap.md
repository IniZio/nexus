# Roadmap

Focus: remote workspace core quality and usability.

**Tracks:** daemon reliability (lifecycle, compose forwarding, readiness); SDK parity (typed APIs, spotlight/readiness, tests); docs aligned with implemented behavior.

**Verify:** `packages/nexus` — `go test ./...`; `packages/sdk/js` — `pnpm exec tsc --noEmit` and `pnpm exec jest --runInBand`.
