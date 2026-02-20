# Large File Operations

Generate and benchmark large files for Nexus performance testing.

## Quick Start

```bash
cd examples/large-file-operations

# Generate test files
node generate-large-files.js

# Run benchmarks
node benchmark.js
```

## Generated Files

The generator creates files in `generated/` directory:

| Category | Files | Size Range |
|----------|-------|------------|
| Small | small.json, small.csv, small.txt | < 1 MB |
| Medium | medium.json, medium.csv, medium.txt | 1-10 MB |
| Large | large.json, large.csv, large.txt | 10-50 MB |
| Many Small | many_small/*, many_medium/* | Various |

## Benchmark Tests

1. **Initial Analysis** - Time to analyze directory structure
2. **File Change Detection** - Latency for change detection
3. **Pattern Search** - Search performance across files
4. **Memory Usage** - RSS memory consumption

## Performance Targets

- Initial analysis: < 30 seconds for 200 MB
- Change detection: < 2 seconds latency
- Pattern search: < 10 seconds for 200 MB
- Memory usage: < 1 GB RSS
