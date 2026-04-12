# Live Port Detection E2E Test Plan

## Overview

Test suite for verifying live port detection functionality where new listening ports automatically appear in `tunnel.list` without manual `tunnel.add()`.

## Test Cases

### 1. Source Field Verification for Manual Forwards
**Test ID:** `spotlight.live-port.maintains-source-field`

Verifies that manually created forwards have correct source field set to "manual".

**Steps:**
1. Create a workspace
2. Manually add a forward using `tunnel.add()`
3. Verify forward has `source: "manual"`
4. List forwards and verify source is persisted

**Expected Results:**
- Manual forward has `source: "manual"`
- Source field is persisted in repository

---

### 2. Port Monitoring Capability Verification
**Test ID:** `spotlight.live-port.auto-detects-new-port`

Verifies that port monitoring capability is available.

**Steps:**
1. Create a workspace
2. Check capabilities for `spotlight.tunnel` and `runtime.seatbelt`
3. Start workspace (triggers port monitoring)
4. List forwards to verify API works

---

## Environment Requirements

- `spotlight.tunnel` capability
- `runtime.seatbelt` for full live port detection

## Test Execution

```bash
cd packages/e2e/flows
pnpm exec jest spotlight-live-port-detection --runInBand --testTimeout=60000
```

## Execution Time

- Test suite: ~25-30 seconds
