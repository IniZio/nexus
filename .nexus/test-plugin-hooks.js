import { NexusEnforcerPlugin } from '../.opencode/plugins/nexus-enforcer.js';
import path from 'path';
import fs from 'fs';
import { fileURLToPath } from 'url';

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);

process.chdir('/home/newman/magic/nexus');

const STATE_PATH = path.join(process.cwd(), '.nexus/boulder/state.json');

function cleanup() {
  if (fs.existsSync(STATE_PATH)) {
    fs.unlinkSync(STATE_PATH);
  }
}

function readState() {
  try {
    if (fs.existsSync(STATE_PATH)) {
      return JSON.parse(fs.readFileSync(STATE_PATH, 'utf8'));
    }
  } catch (e) {
    console.error('Error reading state:', e.message);
  }
  return null;
}

async function runTests() {
  console.log('=== Testing Nexus Enforcer Plugin Hooks ===\n');
  
  cleanup();
  
  const context = { directory: process.cwd() };
  const hooks = await NexusEnforcerPlugin(context);
  
  console.log('Plugin loaded successfully\n');
  
  console.log('--- Test 1: tool.execute.before hook ---');
  const beforeTool = await hooks['tool.execute.before']({ tool: 'read' });
  console.log('tool.execute.before({tool: "read"}) called successfully');
  
  let state = readState();
  console.log('State after tool call:', state);
  
  console.log('\n--- Test 2: response.before hook with completion keywords ---');
  const beforeResponse = await hooks['response.before']({ content: 'I am done' });
  console.log('Hook returned:', beforeResponse?.nexusEnforcement?.triggered ? 'ENFORCEMENT TRIGGERED' : 'No enforcement');
  
  state = readState();
  console.log('State after response:', state);
  
  const enforcementTriggered = beforeResponse?.nexusEnforcement?.triggered === true;
  const stateIteration = state?.iteration > 0;
  
  console.log('\n--- Test 3: Verify state update ---');
  console.log('Iteration incremented:', stateIteration ? 'PASS' : 'FAIL');
  console.log('Enforcement in response:', enforcementTriggered ? 'PASS' : 'FAIL');
  
  console.log('\n--- Test 4: response.before hook with work indicators (should NOT trigger) ---');
  const beforeResponseWork = await hooks['response.before']({ content: 'I am done implementing the feature' });
  console.log('Hook returned:', beforeResponseWork?.nexusEnforcement?.triggered ? 'ENFORCEMENT TRIGGERED (unexpected)' : 'No enforcement (expected)');
  
  console.log('\n--- Test 5: session.status hook ---');
  const status = await hooks['session.status']({});
  console.log('Session status:', status?.nexusEnforcer ? 'Active' : 'Inactive');
  console.log('Iteration in status:', status?.nexusEnforcer?.iteration);
  
  console.log('\n--- Summary ---');
  const allPassed = enforcementTriggered && stateIteration;
  console.log('All tests passed:', allPassed ? 'YES' : 'NO');
  console.log('Enforcement triggers correctly:', enforcementTriggered ? 'YES' : 'NO');
  console.log('State updates correctly:', stateIteration ? 'YES' : 'NO');
  
  console.log('\n--- Final State ---');
  console.log(readState());
}

runTests().catch(console.error);
