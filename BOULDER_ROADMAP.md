# Boulder Implementation Roadmap

This document outlines the phased implementation plan for Boulder enforcement in OpenCode.

---

## Phase 1: MCP Server (Weeks 1-2)

### Goals
Build the MCP server foundation that enables OpenCode to expose Boulder state and controls to external clients.

### Deliverables

#### Week 1: MCP Server Foundation
- [ ] Design MCP server architecture for OpenCode
- [ ] Implement basic MCP server with stdio transport
- [ ] Create MCP tool definitions (schema)
- [ ] Add server registration to OpenCode plugin system

#### Week 2: Session, Message, UI Tools
- [ ] Implement `get_session` tool - retrieve current session info
- [ ] Implement `send_message` tool - inject messages into conversation
- [ ] Implement `show_toast` tool - display UI notifications
- [ ] Implement `get_boulder_state` tool - read enforcement state
- [ ] Implement `trigger_boulder` tool - manually trigger enforcement
- [ ] Add error handling and logging

#### Integration with OpenCode
- [ ] Register MCP server as OpenCode plugin
- [ ] Add configuration for MCP server port/transport
- [ ] Document MCP integration in OpenCode plugins docs

---

## Phase 2: MCP-Based Testing (Week 3)

### Goals
Migrate existing Boulder tests to use MCP for true end-to-end testing.

### Deliverables

#### Migrate Boulder Tests to MCP
- [ ] Create MCP test client library
- [ ] Rewrite `boulder.test.ts` to use MCP tools
- [ ] Add assertions for toast messages
- [ ] Add assertions for system messages
- [ ] Test idle detection via MCP state

#### True E2E Testing
- [ ] Test toast notifications appear correctly
- [ ] Test system messages delivered to conversation
- [ ] Test boulder state transitions
- [ ] Test enforcement triggers at correct intervals

#### CI/CD Integration
- [ ] Add MCP server to test environment
- [ ] Configure CI to run E2E tests
- [ ] Add test coverage for critical paths
- [ ] Set up test reporting

---

## Phase 3: Multi-Boulder (Weeks 4-6)

### Goals
Support multiple parallel boulders with hierarchical enforcement.

### Deliverables

#### Week 4: Multi-Boulder State Design
- [ ] Design state schema for multiple boulders
- [ ] Implement `BoulderManager` class
- [ ] Add boulder registry (active boulders tracking)
- [ ] Implement parent-child relationship model

#### Week 5: Hierarchical Enforcement
- [ ] Implement parent boulder enforcement logic
- [ ] Implement child boulder enforcement logic
- [ ] Add coordination between parent and children
- [ ] Handle boulder completion/completion detection

#### Week 6: Testing via MCP
- [ ] Add MCP tools for multi-boulder management
- [ ] Create `create_boulder`, `list_boulders`, `switch_boulder` tools
- [ ] Write E2E tests for multi-boulder scenarios
- [ ] Test parent-child relationships
- [ ] Test hierarchical enforcement

---

## Dependencies

| Phase | Dependency |
|-------|-------------|
| Phase 1 | OpenCode plugin system knowledge |
| Phase 2 | Phase 1 MCP server |
| Phase 3 | Phase 2 MCP testing infrastructure |

## Success Criteria

- [ ] MCP server runs in OpenCode and responds to tools
- [ ] E2E tests pass via MCP (no internal APIs)
- [ ] Multiple boulders can run in parallel
- [ ] Parent boulders correctly track child completion
- [ ] All tests run in CI/CD pipeline
