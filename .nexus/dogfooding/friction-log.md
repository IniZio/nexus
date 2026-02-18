# Dogfooding Friction Log

## Session: 2026-02-18 - Phase 8 Real Testing

### Telemetry
- ✅ Enabled successfully
- ✅ Message about data location is clear

### Doc Creation
- ✅ Created 3 docs successfully:
  1. task-1771416160151861297: "Installing Nexus on macOS" (tutorial)
  2. task-1771416161517898229: "Debugging Port Conflicts" (how-to)
  3. task-1771416162556955794: "Architecture Overview" (explanation)

### Bugs Found

**Bug #1: SQL NULL handling in doc list**
- Command: `nexus doc list`
- Error: `sql: Scan error on column index 8, name "verification_by": converting NULL to string is unsupported`
- Cause: Database returns NULL for unset fields, but code expects strings
- Severity: HIGH - blocks doc listing

**Bug #2: SQL NULL handling in stats**
- Command: `nexus stats`
- Error: `converting NULL to int64 is unsupported`
- Cause: No telemetry data yet, AVG() returns NULL
- Severity: MEDIUM - should handle empty data gracefully

### Next Steps
- Fix NULL handling in database queries
- Add proper sql.NullString, sql.NullInt64 usage
- Test doc list and stats again
