/** @type {import('jest').Config} */
export default {
  testEnvironment: "node",
  rootDir: "./boulder",
  testMatch: ["**/*.test.ts"],
  transform: {
    "^.+\\.ts$": ["ts-jest", {
      "tsconfig": "../tsconfig.json"
    }]
  },
  moduleFileExtensions: ["ts", "js", "json"],
  collectCoverageFrom: [
    "**/*.ts",
    "!node_modules/**",
    "!dist/**"
  ],
  coverageDirectory: "../coverage",
  verbose: true,
  testTimeout: 60000
};
