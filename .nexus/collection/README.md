# Nexus Friction Collection System

Document friction, issues, and edge cases encountered while using Nexus.

## What is Friction Collection?

Friction collection captures:
- Unexpected behavior or bugs
- Performance issues or slowdowns
- Missing features or confusing UX
- Edge cases that don't work as expected
- Error messages that are unclear
- Any other pain points during usage

## Directory Structure

```
.nexus/collection/
├── README.md                    # This file
├── templates/                   # Report templates
│   ├── friction-report.md
│   ├── trace-session.md
│   └── issue-reproduction.md
├── schemas/                     # JSON schemas for validation
│   ├── friction.schema.json
│   └── trace.schema.json
├── data/                        # Collected friction data
│   └── friction-*.json
├── sessions/                    # Trace session logs
│   └── session-*.json
└── .gitignore                   # Prevents committing sensitive data
```

## Quick Start

### 1. Document Friction

When you encounter an issue:

```bash
# Copy the template
cp .nexus/collection/templates/friction-report.md friction-report-$(date +%Y%m%d).md

# Edit with your findings
vim friction-report-$(date +%Y%m%d).md

# Move to collection
mv friction-report-*.md .nexus/collection/data/
```

### 2. Record a Trace Session

For reproducible issues:

```bash
# Copy the trace session template
cp .nexus/collection/templates/trace-session.md trace-session-$(date +%Y%m%d-%H%M%S).md

# Document steps to reproduce
# Include command outputs and timestamps

# Move to sessions folder
mv trace-session-*.md .nexus/collection/sessions/
```

### 3. Reproduce an Issue

Follow the issue reproduction template for complex bugs:

```bash
cp .nexus/collection/templates/issue-reproduction.md issue-$(date +%Y%m%d).md
```

## Templates

### friction-report.md
For general friction points and usability issues.

**Fields:**
- Title: Brief description
- Severity: low / medium / high / critical
- Category: performance / bug / missing-feature / documentation / other
- Environment: OS, Node version, Nexus version
- Description: Detailed explanation
- Expected: What should happen
- Actual: What actually happens
- Impact: User experience effect
- Workaround: Any known solutions

### trace-session.md
For recording session traces with timestamps.

**Fields:**
- Session ID: Unique identifier
- Start/End time
- Environment details
- Commands executed
- Outputs and errors
- Observations

### issue-reproduction.md
For step-by-step bug reproduction.

**Fields:**
- Issue summary
- Environment
- Reproduction steps (numbered)
- Expected result
- Actual result
- Screenshots/logs
- Related issues

## Data Collection Guidelines

### What to Collect
- Error messages and stack traces
- Command outputs
- Timing information
- File changes detected
- Any unexpected behavior
- Performance metrics

### What NOT to Collect
- Personal identifying information
- API keys or secrets
- Passwords or credentials
- Private repository names
- Sensitive file contents
- Anything in `.env` files

## Privacy Considerations

1. **Automatic Filtering**: The collection system automatically ignores:
   - `.env` files
   - `.git/` directories
   - `node_modules/`
   - Files matching `*.local.*`
   - Files starting with `.local`

2. **Manual Review**: Before submitting, review your reports for:
   - Hardcoded credentials
   - Internal hostnames/IPs
   - Proprietary code snippets
   - Personal information

3. **Anonymization**: Use placeholders for sensitive data:
   - `[REDACTED]` for credentials
   - `<PROJECT_NAME>` for private repo names
   - `[IP_ADDRESS]` for network info

## Submitting Reports

1. Ensure no sensitive data is included
2. Use the appropriate template
3. Include reproduction steps when possible
4. Add severity and category labels
5. Move completed reports to `.nexus/collection/data/`

## Best Practices

- Document issues immediately when encountered
- Include timestamps for time-sensitive issues
- Save command outputs verbatim (don't paraphrase)
- Categorize by severity to prioritize fixes
- Link related issues together
- Update as new information becomes available

## Schema Validation

Reports can be validated against JSON schemas:

```bash
# Validate a friction report
npm run validate -- --schema .nexus/collection/schemas/friction.schema.json --file .nexus/collection/data/friction-report.json
```

## Integration with Development

- Use friction logs during retrospectives
- Track recurring issues across projects
- Identify patterns in user friction
- Prioritize improvements based on impact
