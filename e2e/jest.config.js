// DEPRECATED: E2E tests are disabled - workspace-sdk package was deleted
// See README.md for details on re-enabling

module.exports = {
  preset: 'ts-jest',
  testEnvironment: 'node',
  roots: ['<rootDir>/tests'],
  // Disable all test matching - no tests will run
  testMatch: ['**/*.disabled.ts'],
  // Keep original config commented for reference when re-enabling:
  // testMatch: ['**/*.test.ts'],
  // setupFilesAfterEnv: ['<rootDir>/tests/setup.ts'],
  collectCoverageFrom: [],
  coverageDirectory: 'coverage',
  coverageReporters: ['text', 'lcov', 'html'],
  // Removed: moduleNameMapper for @nexus/workspace-sdk (package deleted)
  testTimeout: 60000,
  globalTimeout: 300000,
  verbose: true,
};
