# Boulder Continuous Enforcement

The Boulder is a continuous enforcement system that ensures agents never stop improving.

## Core Principle

**The boulder NEVER stops rolling.**

## How It Works

1. **Iteration Tracking**: Every validation increments an iteration counter
2. **Minimum Iterations**: Completion is blocked until at least 5 iterations
3. **Consecutive Attempts**: 3 consecutive completion attempts trigger a block
4. **Improvement Tasks**: Random tasks are generated to guide continuous improvement

## Usage

### Basic Validation

```typescript
import { createNexusEnforcer } from 'nexus-enforcer';

const enforcer = createNexusEnforcer();

// Before starting work
const beforeResult = enforcer.validateBefore({
  taskDescription: 'Implement feature X',
  agentType: 'opencode',
});

// After completing work
const afterResult = enforcer.validateAfter({
  taskDescription: 'Implement feature X',
  agentType: 'opencode',
});
```

### Enforcing Completion

```typescript
// When agent attempts to complete
try {
  enforcer.enforceCompletion();
  // Completion is allowed
} catch (error) {
  // Completion blocked - continue improving
  console.error(error.message);
}
```

### Checking State

```typescript
if (enforcer.canComplete()) {
  console.log('Completion allowed');
}

const state = enforcer.getBoulderState();
console.log(`Iteration: ${state.iteration}`);
```

## Enforcement Levels

1. **FORCED_CONTINUATION** (Iterations 1-4): Completion strictly blocked
2. **ALLOWED** (Iterations 5+): Completion possible if no errors
3. **BLOCKED** (After 3 consecutive attempts): Completion blocked

## Improvement Tasks

- Write additional test cases
- Refactor code for better performance
- Research best practices for current implementation
- Optimize for edge cases
- Add comprehensive error handling
- Improve documentation
- Review code for security issues
- Add monitoring and observability

## Philosophy

> The boulder never stops rolling.
> Every task can be improved.
> Every iteration adds value.
