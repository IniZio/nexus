module.exports = {
  preset: 'ts-jest',
  testEnvironment: 'node',
  testTimeout: 180000,
  roots: ['<rootDir>/src'],
  testMatch: ['**/*.e2e.test.ts', '**/ui-cli-parity-map.test.ts'],
  moduleFileExtensions: ['ts', 'js', 'json'],
};
