#!/usr/bin/env node
/**
 * MCP Boulder Test Runner
 * 
 * This script runs the MCP-based tests for boulder functionality.
 * It spawns the MCP server and runs Jest tests against it.
 */

import { spawn } from "child_process";
import { readFileSync, existsSync } from "fs";
import { join, dirname } from "path";
import { fileURLToPath } from "url";

const __dirname = dirname(fileURLToPath(import.meta.url));
const SERVER_PATH = join(__dirname, "../server/dist/index.js");
const TESTS_PATH = join(__dirname, "boulder");

async function runTests(): Promise<void> {
  console.log("Starting MCP Boulder Tests...\n");

  const serverBuildExists = existsSync(SERVER_PATH);
  if (!serverBuildExists) {
    console.error("Error: MCP server not built. Run 'npm run build' in the server directory first.");
    process.exit(1);
  }

  console.log("1. Starting MCP server...");
  const server = spawn("node", [SERVER_PATH], {
    stdio: ["pipe", "pipe", "pipe"],
    cwd: __dirname
  });

  server.stdout.on("data", (data) => {
    console.log(`[Server] ${data.toString().trim()}`);
  });

  server.stderr.on("data", (data) => {
    console.error(`[Server Error] ${data.toString().trim()}`);
  });

  await new Promise(resolve => setTimeout(resolve, 2000));

  console.log("\n2. Running idle detection tests...");
  const idleTestResult = await runJestTest(join(TESTS_PATH, "idle-detection.test.ts"));

  console.log("\n3. Running cooldown tests...");
  const cooldownTestResult = await runJestTest(join(TESTS_PATH, "cooldown.test.ts"));

  console.log("\n4. Running completion detection tests...");
  const completionTestResult = await runJestTest(join(TESTS_PATH, "completion-detection.test.ts"));

  server.kill();

  console.log("\n=== Test Summary ===");
  console.log(`Idle Detection: ${idleTestResult ? "PASSED" : "FAILED"}`);
  console.log(`Cooldown: ${cooldownTestResult ? "PASSED" : "FAILED"}`);
  console.log(`Completion Detection: ${completionTestResult ? "PASSED" : "FAILED"}`);

  const allPassed = idleTestResult && cooldownTestResult && completionTestResult;
  process.exit(allPassed ? 0 : 1);
}

async function runJestTest(testPath: string): Promise<boolean> {
  return new Promise((resolve) => {
    const jest = spawn("npx", ["jest", testPath, "--testTimeout=60000"], {
      stdio: "inherit",
      cwd: __dirname
    });

    jest.on("close", (code) => {
      resolve(code === 0);
    });

    jest.on("error", (err) => {
      console.error(`Test execution error: ${err.message}`);
      resolve(false);
    });
  });
}

runTests().catch((err) => {
  console.error("Test runner error:", err);
  process.exit(1);
});
