# Web Dashboard PRD

**Status:** Draft  
**Created:** 2026-02-23  
**Component:** Web Dashboard  
**Priority:** P1  

---

## 1. Overview

### 1.1 Problem Statement

Nexus is currently CLI-only, requiring users to:
- Remember complex command syntax
- Parse text output to understand workspace status
- Execute commands to check resource usage
- Lack visual overview of workspace fleet

### 1.2 Goals

1. **Visual Management** - Create, monitor, and manage workspaces via web UI
2. **Real-Time Monitoring** - Live resource usage (CPU, memory, disk, network)
3. **Team Visibility** - See shared workspaces and team member activity
4. **Quick Actions** - One-click start/stop/SSH with terminal in browser
5. **Accessibility** - Works on desktop, tablet, and mobile

### 1.3 Non-Goals

- Full IDE in browser (use VS Code Server separately)
- Complex analytics/BI (Phase 2)
- User management (handled by multi-user PRD)
- Billing dashboard (Phase 3)

---

## 2. Architecture

### 2.1 System Overview

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                         Nexus Web Dashboard                                 │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│  ┌─────────────────────────────────────────────────────────────────────┐   │
│  │                        Browser Client                                │   │
│  │  ┌──────────────┐  ┌──────────────┐  ┌──────────────────────────┐  │   │
│  │  │   React App  │  │   WebSocket  │  │   Terminal (xterm.js)    │  │   │
│  │  │   (Vite)     │  │   Client     │  │                          │  │   │
│  │  └──────┬───────┘  └──────┬───────┘  └────────────┬─────────────┘  │   │
│  └─────────┼────────────────┼──────────────────────┼────────────────┘   │
│            │                │                      │                     │
│            ▼                ▼                      ▼                     │
│  ┌─────────────────────────────────────────────────────────────────────┐   │
│  │                        nexusd Server                                 │   │
│  │  ┌──────────────┐  ┌──────────────┐  ┌──────────────────────────┐  │   │
│  │  │   Static     │  │   REST API   │  │   WebSocket              │  │   │
│  │  │   Assets     │  │   Handler    │  │   Handler                │  │   │
│  │  └──────────────┘  └──────────────┘  └──────────────────────────┘  │   │
│  └─────────────────────────────────────────────────────────────────────┘   │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

### 2.2 Technology Stack

| Layer | Technology | Reason |
|-------|------------|--------|
| Framework | React 18 | Industry standard, excellent ecosystem |
| Language | TypeScript | Type safety, better DX |
| Styling | Tailwind CSS | Utility-first, rapid development |
| State | Zustand | Lightweight, TypeScript-friendly |
| Data Fetching | React Query | Caching, background updates |
| Routing | React Router v6 | Declarative routing |
| Charts | Recharts | React-native, customizable |
| Terminal | xterm.js | VS Code's terminal component |
| Build | Vite | Fast dev, optimized builds |
| Icons | Lucide React | Clean, consistent icons |

### 2.3 API Integration

The dashboard uses existing nexusd APIs:

```
REST API (HTTP):
  GET  /api/v1/workspaces
  POST /api/v1/workspaces
  GET  /api/v1/workspaces/:id
  POST /api/v1/workspaces/:id/start
  POST /api/v1/workspaces/:id/stop
  GET  /api/v1/workspaces/:id/logs

WebSocket (Real-time):
  /ws - Events, metrics, terminal
```

New endpoints needed:
```
GET /api/v1/metrics/:workspace_id - Resource usage metrics
GET /api/v1/events - Audit log events
```

---

## 3. Design Specification

### 3.1 Page Structure

```
/
├── /login              # Authentication
├── /                   # Dashboard (workspace list)
├── /workspaces/:id     # Workspace detail
├── /workspaces/new     # Create workspace
├── /settings           # User/Org settings
└── /logs               # Audit logs (admin)
```

### 3.2 Component Hierarchy

```
App
├── Layout
│   ├── Sidebar
│   │   ├── Logo
│   │   ├── NavLinks
│   │   └── UserMenu
│   └── Header
│       ├── Search
│       ├── Notifications
│       └── Profile
├── Pages
│   ├── Dashboard
│   │   ├── StatsCards
│   │   ├── WorkspaceList
│   │   │   ├── WorkspaceCard
│   │   │   └── WorkspaceRow (table view)
│   │   └── CreateWorkspaceButton
│   ├── WorkspaceDetail
│   │   ├── Header
│   │   ├── StatusBadge
│   │   ├── ResourceCharts
│   │   ├── TerminalPanel
│   │   ├── LogsPanel
│   │   └── SettingsPanel
│   ├── CreateWorkspace
│   │   ├── TemplateSelector
│   │   ├── ResourceForm
│   │   └── GitImportForm
│   └── Settings
│       ├── ProfileForm
│       ├── OrgSettings
│       └── Preferences
└── Shared
    ├── Button
    ├── Card
    ├── Modal
    ├── Toast
    ├── LoadingSpinner
    └── ErrorBoundary
```

### 3.3 Key Screens

#### Dashboard (Workspace List)

```
┌────────────────────────────────────────────────────────────────────┐
│  Nexus                                      [Search] [Bell] [User] │
├──────────┬─────────────────────────────────────────────────────────┤
│          │  Workspaces                                [+ New]     │
│  Logo    │  ┌───────────────────────────────────────────────────┐ │
│          │  │ Filter: [All ▼]  View: [Cards ▼]  Sort: [Newest ▼]│ │
│  ──────  │  └───────────────────────────────────────────────────┘ │
│  Dashboard│                                                          │
│  ──────  │  ┌─────────────┐ ┌─────────────┐ ┌─────────────┐       │
│  Teams   │  │  feature-   │ │  bugfix-    │ │  api-       │       │
│  Logs    │  │  auth       │ │  login      │ │  refactor   │       │
│  Settings│  │  🟢 Running │ │  🟡 Sleeping│ │  🔴 Stopped │       │
│          │  │             │ │             │ │             │       │
│          │  │ CPU: 12%    │ │ CPU: 0%     │ │ -           │       │
│          │  │ Mem: 45%    │ │ Mem: 0%     │ │ -           │       │
│          │  │             │ │             │ │             │       │
│          │  │ [Open]      │ │ [Start]     │ │ [Start]     │       │
│          │  └─────────────┘ └─────────────┘ └─────────────┘       │
│          │                                                          │
└──────────┴──────────────────────────────────────────────────────────┘
```

#### Workspace Detail

```
┌────────────────────────────────────────────────────────────────────┐
│  ← Back to Workspaces    feature-auth            [Start] [Stop] [⋯] │
├────────────────────────────────────────────────────────────────────┤
│                                                                    │
│  Status: 🟢 Running  Owner: jane@acme.com  Created: 2 days ago     │
│                                                                    │
│  ┌─────────────────────┐  ┌─────────────────────────────────────┐  │
│  │   Resource Usage    │  │   Terminal                          │  │
│  │                     │  │                                     │  │
│  │  [CPU Chart]        │  │  nexus@workspace:~$                 │  │
│  │  34% avg            │  │  ls -la                             │  │
│  │                     │  │  total 128                          │  │
│  │  [Memory Chart]     │  │  drwxr-xr-x  5 nexus nexus 4096 ... │  │
│  │  2.1GB / 4GB        │  │                                     │  │
│  │                     │  │  nexus@workspace:~$ _               │  │
│  │  [Disk Chart]       │  │                                     │  │
│  │  12GB / 20GB        │  │                                     │  │
│  │                     │  │                                     │  │
│  └─────────────────────┘  └─────────────────────────────────────┘  │
│                                                                    │
│  ┌─────────────────────────────────────────────────────────────┐   │
│  │  Recent Logs                                                │   │
│  │  ─────────────────────────────────────────────────────────  │   │
│  │  2026-02-23 14:32:01  Container started                     │   │
│  │  2026-02-23 14:32:05  SSH server ready on port 32801       │   │
│  │  2026-02-23 14:32:08  File sync initialized                │   │
│  └─────────────────────────────────────────────────────────────┘   │
│                                                                    │
└────────────────────────────────────────────────────────────────────┘
```

#### Create Workspace

```
┌────────────────────────────────────────────────────────────────────┐
│  ← Back                              Create New Workspace          │
├────────────────────────────────────────────────────────────────────┤
│                                                                    │
│  1. Choose Template                                                │
│  ┌──────────┐ ┌──────────┐ ┌──────────┐ ┌──────────┐              │
│  │   Node   │ │  Python  │ │    Go    │ │  Custom  │              │
│  │  [icon]  │ │  [icon]  │ │  [icon]  │ │  [icon]  │              │
│  │ 18.x     │ │ 3.11     │ │ 1.21     │ │ Blank    │              │
│  └──────────┘ └──────────┘ └──────────┘ └──────────┘              │
│                                                                    │
│  2. Configure                                                      │
│  ┌─────────────────────────────────────────────────────────────┐  │
│  │  Name: [feature-api                              ]          │  │
│  │  Branch: [main                                   ]          │  │
│  │                                                             │  │
│  │  Resources:                                                 │  │
│  │  CPU:  [●──────○──────○]  2 cores    (max 8)                │  │
│  │  RAM:  [●──────○──────○]  4 GB       (max 16)               │  │
│  │  Disk: [●──────○──────○]  20 GB      (max 100)              │  │
│  └─────────────────────────────────────────────────────────────┘  │
│                                                                    │
│                        [Cancel]  [Create Workspace]               │
│                                                                    │
└────────────────────────────────────────────────────────────────────┘
```

---

## 4. API Specification

### 4.1 Metrics API

**Get Workspace Metrics:**
```http
GET /api/v1/metrics/:workspace_id
Authorization: Bearer <token>

Response:
{
  "workspace_id": "ws-123",
  "timestamp": "2026-02-23T14:30:00Z",
  "cpu": {
    "usage_percent": 34.5,
    "cores_used": 0.69,
    "cores_total": 2
  },
  "memory": {
    "used_bytes": 2254857830,
    "total_bytes": 4294967296,
    "usage_percent": 52.5
  },
  "disk": {
    "used_bytes": 12884901888,
    "total_bytes": 21474836480,
    "usage_percent": 60.0
  },
  "network": {
    "rx_bytes_per_sec": 1024000,
    "tx_bytes_per_sec": 512000
  },
  "history": [
    {
      "timestamp": "2026-02-23T14:25:00Z",
      "cpu_percent": 32.1,
      "memory_percent": 51.2
    }
  ]
}
```

### 4.2 WebSocket Protocol

**Connection:**
```
wss://localhost:9847/ws?token=<jwt>
```

**Client → Server:**
```json
// Subscribe to workspace updates
{
  "type": "subscribe",
  "channel": "workspace:ws-123"
}

// Execute command in terminal
{
  "type": "terminal:input",
  "workspace_id": "ws-123",
  "data": "ls -la\n"
}
```

**Server → Client:**
```json
// Workspace status update
{
  "type": "workspace:status",
  "workspace_id": "ws-123",
  "data": {
    "status": "running",
    "updated_at": "2026-02-23T14:30:00Z"
  }
}

// Metrics update
{
  "type": "workspace:metrics",
  "workspace_id": "ws-123",
  "data": {
    "cpu_percent": 34.5,
    "memory_percent": 52.5
  }
}

// Terminal output
{
  "type": "terminal:output",
  "workspace_id": "ws-123",
  "data": "total 128\ndrwxr-xr-x  5 nexus nexus 4096 ..."
}
```

### 4.3 Events API

**Get Audit Events:**
```http
GET /api/v1/events?limit=50&offset=0
Authorization: Bearer <token>

Response:
{
  "events": [
    {
      "id": "evt-123",
      "type": "workspace.created",
      "actor": {
        "id": "usr-456",
        "email": "jane@acme.com"
      },
      "resource": {
        "type": "workspace",
        "id": "ws-123",
        "name": "feature-auth"
      },
      "metadata": {
        "template": "node-postgres",
        "cpu_cores": 2,
        "memory_gb": 4
      },
      "created_at": "2026-02-23T14:30:00Z"
    }
  ],
  "total": 156,
  "limit": 50,
  "offset": 0
}
```

---

## 5. Frontend Architecture

### 5.1 Project Structure

```
packages/dashboard/
├── src/
│   ├── components/
│   │   ├── ui/              # Reusable UI components
│   │   │   ├── Button.tsx
│   │   │   ├── Card.tsx
│   │   │   └── Modal.tsx
│   │   ├── layout/          # Layout components
│   │   │   ├── Sidebar.tsx
│   │   │   └── Header.tsx
│   │   └── features/        # Feature-specific components
│   │       ├── WorkspaceCard.tsx
│   │       ├── ResourceCharts.tsx
│   │       └── Terminal.tsx
│   ├── hooks/
│   │   ├── useWorkspaces.ts
│   │   ├── useWorkspace.ts
│   │   ├── useMetrics.ts
│   │   └── useWebSocket.ts
│   ├── lib/
│   │   ├── api.ts           # API client
│   │   ├── websocket.ts     # WebSocket client
│   │   └── utils.ts
│   ├── pages/
│   │   ├── Dashboard.tsx
│   │   ├── WorkspaceDetail.tsx
│   │   ├── CreateWorkspace.tsx
│   │   └── Settings.tsx
│   ├── store/
│   │   └── authStore.ts     # Zustand store
│   ├── types/
│   │   └── index.ts
│   ├── App.tsx
│   └── main.tsx
├── index.html
├── package.json
├── tailwind.config.js
├── tsconfig.json
└── vite.config.ts
```

### 5.2 State Management

```typescript
// store/authStore.ts
import { create } from 'zustand';

interface AuthState {
  user: User | null;
  organization: Organization | null;
  token: string | null;
  isAuthenticated: boolean;
  login: (email: string, password: string) => Promise<void>;
  logout: () => void;
  setOrganization: (org: Organization) => void;
}

export const useAuthStore = create<AuthState>((set) => ({
  user: null,
  organization: null,
  token: localStorage.getItem('nexus_token'),
  isAuthenticated: !!localStorage.getItem('nexus_token'),
  login: async (email, password) => {
    const response = await api.post('/auth/login', { email, password });
    localStorage.setItem('nexus_token', response.token);
    set({ user: response.user, token: response.token, isAuthenticated: true });
  },
  logout: () => {
    localStorage.removeItem('nexus_token');
    set({ user: null, token: null, isAuthenticated: false });
  },
  setOrganization: (org) => set({ organization: org }),
}));
```

### 5.3 API Client

```typescript
// lib/api.ts
import axios from 'axios';

const api = axios.create({
  baseURL: '/api/v1',
  headers: {
    'Content-Type': 'application/json',
  },
});

api.interceptors.request.use((config) => {
  const token = localStorage.getItem('nexus_token');
  if (token) {
    config.headers.Authorization = `Bearer ${token}`;
  }
  return config;
});

api.interceptors.response.use(
  (response) => response.data,
  (error) => {
    if (error.response?.status === 401) {
      localStorage.removeItem('nexus_token');
      window.location.href = '/login';
    }
    return Promise.reject(error);
  }
);

export default api;
```

---

## 6. Implementation Phases

### Phase 1: Foundation (Week 1-2)

- [ ] Set up React + TypeScript + Vite project
- [ ] Configure Tailwind CSS
- [ ] Set up React Router
- [ ] Create base UI components (Button, Card, Modal)
- [ ] Implement auth context and login page

### Phase 2: Dashboard Layout (Week 3)

- [ ] Create sidebar navigation
- [ ] Create header with search/notifications
- [ ] Implement responsive layout
- [ ] Add dark mode support
- [ ] Error boundaries and loading states

### Phase 3: Workspace List (Week 4)

- [ ] Fetch and display workspaces
- [ ] Workspace cards with status
- [ ] Filter and sort functionality
- [ ] Grid/list view toggle
- [ ] Quick action buttons (start/stop)

### Phase 4: Real-Time Updates (Week 5)

- [ ] WebSocket connection management
- [ ] Subscribe to workspace events
- [ ] Live status updates
- [ ] Toast notifications for events

### Phase 5: Workspace Detail (Week 6-7)

- [ ] Workspace detail page
- [ ] Resource usage charts (Recharts)
- [ ] Logs viewer
- [ ] Settings panel
- [ ] Delete confirmation

### Phase 6: Terminal Integration (Week 8)

- [ ] xterm.js integration
- [ ] WebSocket-based terminal
- [ ] Terminal in workspace detail
- [ ] Multi-tab support (stretch)

### Phase 7: Create Workspace (Week 9)

- [ ] Template selection UI
- [ ] Resource configuration sliders
- [ ] Git import form
- [ ] Validation and error handling

### Phase 8: Polish (Week 10)

- [ ] Mobile responsiveness
- [ ] Performance optimization
- [ ] Accessibility (a11y)
- [ ] E2E tests with Playwright

---

## 7. Build and Deployment

### 7.1 Build Process

```json
// package.json
{
  "scripts": {
    "dev": "vite",
    "build": "tsc && vite build",
    "preview": "vite preview",
    "lint": "eslint . --ext ts,tsx",
    "test": "vitest",
    "test:e2e": "playwright test"
  }
}
```

### 7.2 Integration with nexusd

The dashboard is served as static files by nexusd:

```go
// In nexusd server.go
func (s *Server) registerHTTPRoutes() {
    // API routes
    s.mux.HandleFunc("/api/v1/workspaces", s.handleWorkspaces)
    // ... other API routes
    
    // Static dashboard files
    fs := http.FileServer(http.Dir("./dashboard/dist"))
    s.mux.Handle("/", fs)
}
```

### 7.3 Development Workflow

```bash
# Terminal 1: Start nexusd
nexus daemon

# Terminal 2: Start dashboard dev server
cd packages/dashboard
npm run dev

# Dashboard available at http://localhost:5173
# API proxied to http://localhost:9847
```

---

## 8. Success Criteria

- [ ] Dashboard loads in < 2 seconds
- [ ] Real-time updates within 1 second
- [ ] All CLI features accessible via UI
- [ ] Works on mobile devices
- [ ] WebSocket reconnection on network loss
- [ ] 90%+ test coverage for critical paths
- [ ] WCAG 2.1 AA accessibility compliance

---

## 9. Future Enhancements

- **Custom Dashboards** - User-configurable layouts
- **Analytics** - Usage trends, cost projections
- **Team Presence** - See who's online/active
- **Screen Sharing** - Collaborative debugging
- **Mobile App** - Native iOS/Android apps

---

**Last Updated:** February 2026
