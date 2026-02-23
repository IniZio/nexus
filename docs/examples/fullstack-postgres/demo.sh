#!/bin/bash
set -e

echo "🚀 Fullstack + PostgreSQL Workspace Demo"
echo "========================================="

WORKSPACE_NAME="${1:-fullstack-demo}"

echo "Step 1: Creating workspace with DinD..."
nexus workspace create "$WORKSPACE_NAME" --dind

echo -e "\nStep 2: Setting up project structure..."

# Copy Dockerfile
nexus workspace exec "$WORKSPACE_NAME" -- sh -c 'cat > /workspace/Dockerfile' < Dockerfile

# Copy docker-compose
nexus workspace exec "$WORKSPACE_NAME" -- sh -c 'cat > /workspace/docker-compose.yml' < docker-compose.yml

# Create directories
nexus workspace exec "$WORKSPACE_NAME" -- mkdir -p /workspace/frontend/src
nexus workspace exec "$WORKSPACE_NAME" -- mkdir -p /workspace/backend/src /workspace/backend/migrations

echo -e "\nStep 3: Creating frontend..."

# Frontend package.json
cat > /tmp/frontend-package.json << 'EOF'
{
  "name": "frontend",
  "private": true,
  "version": "0.0.0",
  "type": "module",
  "scripts": {
    "dev": "vite --host 0.0.0.0 --port 5173"
  },
  "dependencies": {
    "react": "^18.2.0",
    "react-dom": "^18.2.0"
  },
  "devDependencies": {
    "@vitejs/plugin-react": "^4.2.1",
    "vite": "^5.0.8"
  }
}
EOF

nexus workspace exec "$WORKSPACE_NAME" -- sh -c 'cat > /workspace/frontend/package.json' < /tmp/frontend-package.json

# Frontend App.jsx
cat > /tmp/App.jsx << 'EOF'
import { useState, useEffect } from 'react'

function App() {
  const [users, setUsers] = useState([])
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    fetch(import.meta.env.VITE_API_URL + '/api/users')
      .then(r => r.json())
      .then(data => {
        setUsers(data)
        setLoading(false)
      })
  }, [])

  if (loading) return <div>Loading...</div>

  return (
    <div style={{ padding: '2rem', fontFamily: 'sans-serif' }}>
      <h1>🚀 Fullstack Nexus App</h1>
      <h2>Users from Database:</h2>
      <ul>
        {users.map(user => (
          <li key={user.id}>{user.name} ({user.email})</li>
        ))}
      </ul>
    </div>
  )
}

export default App
EOF

nexus workspace exec "$WORKSPACE_NAME" -- sh -c 'cat > /workspace/frontend/src/App.jsx' < /tmp/App.jsx

# Frontend main.jsx
cat > /tmp/main.jsx << 'EOF'
import React from 'react'
import ReactDOM from 'react-dom/client'
import App from './App.jsx'

ReactDOM.createRoot(document.getElementById('root')).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
)
EOF

nexus workspace exec "$WORKSPACE_NAME" -- sh -c 'cat > /workspace/frontend/src/main.jsx' < /tmp/main.jsx

# Frontend index.html
cat > /tmp/index.html << 'EOF'
<!DOCTYPE html>
<html lang="en">
  <head>
    <meta charset="UTF-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1.0" />
    <title>Nexus Fullstack</title>
  </head>
  <body>
    <div id="root"></div>
    <script type="module" src="/src/main.jsx"></script>
  </body>
</html>
EOF

nexus workspace exec "$WORKSPACE_NAME" -- sh -c 'cat > /workspace/frontend/index.html' < /tmp/index.html

echo -e "\nStep 4: Creating backend..."

# Backend package.json
cat > /tmp/backend-package.json << 'EOF'
{
  "name": "backend",
  "version": "1.0.0",
  "scripts": {
    "dev": "node src/server.js"
  },
  "dependencies": {
    "express": "^4.18.2",
    "cors": "^2.8.5",
    "pg": "^8.11.3"
  }
}
EOF

nexus workspace exec "$WORKSPACE_NAME" -- sh -c 'cat > /workspace/backend/package.json' < /tmp/backend-package.json

# Backend server.js
cat > /tmp/server.js << 'EOF'
const express = require('express')
const cors = require('cors')

const app = express()
app.use(cors())
app.use(express.json())

// Health check
app.get('/health', (req, res) => {
  res.json({ status: 'OK', timestamp: new Date() })
})

// Get users (mock - would connect to real DB)
app.get('/api/users', (req, res) => {
  res.json([
    { id: 1, name: 'Alice', email: 'alice@example.com' },
    { id: 2, name: 'Bob', email: 'bob@example.com' }
  ])
})

const PORT = process.env.PORT || 3000
app.listen(PORT, '0.0.0.0', () => {
  console.log(`API server on port ${PORT}`)
})
EOF

nexus workspace exec "$WORKSPACE_NAME" -- sh -c 'cat > /workspace/backend/src/server.js' < /tmp/server.js

# Database init
cat > /tmp/001_init.sql << 'EOF'
CREATE TABLE IF NOT EXISTS users (
  id SERIAL PRIMARY KEY,
  name VARCHAR(100) NOT NULL,
  email VARCHAR(100) UNIQUE NOT NULL,
  created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

INSERT INTO users (name, email) VALUES 
  ('Alice', 'alice@example.com'),
  ('Bob', 'bob@example.com')
ON CONFLICT DO NOTHING;
EOF

nexus workspace exec "$WORKSPACE_NAME" -- sh -c 'cat > /workspace/backend/migrations/001_init.sql' < /tmp/001_init.sql

echo -e "\n✅ Fullstack workspace ready!"
echo "Start services: nexus workspace ssh $WORKSPACE_NAME && docker-compose up -d"
echo "Add ports:"
echo "  nexus workspace port add $WORKSPACE_NAME 5173  # Frontend"
echo "  nexus workspace port add $WORKSPACE_NAME 3000  # API"
echo "  nexus workspace port add $WORKSPACE_NAME 5432  # Database"
echo ""
echo "Open: http://localhost:5173"
