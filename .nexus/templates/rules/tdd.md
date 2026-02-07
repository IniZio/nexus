# Test-Driven Development (TDD)

## TDD Cycle
1. **RED**: Write a failing test first
2. **GREEN**: Implement minimal code to pass the test
3. **REFACTOR**: Clean up code while keeping tests green

## Testing Guidelines
- Use 'testify/assert' and 'testify/require' in Go tests
- Test file naming: '*_test.go' alongside source
- Aim for 80%+ test coverage on new code
- Test both happy paths and error cases
- Use table-driven tests for multiple scenarios

## Benefits
- Ensures code reliability
- Guides design decisions
- Provides safety net for refactoring
- Documents expected behavior through tests
