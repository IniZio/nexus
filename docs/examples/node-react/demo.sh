#!/bin/bash
set -e

echo "🚀 Node + React Workspace Demo"
echo "==============================="

WORKSPACE_NAME="${1:-react-demo}"

echo "Step 1: Creating workspace with DinD..."
nexus workspace create "$WORKSPACE_NAME" --dind

echo -e "\nStep 2: Copying project files..."
# In real usage, user would have their own files
# This is just for the demo
nexus workspace exec "$WORKSPACE_NAME" -- mkdir -p /workspace/src

cat > /tmp/package.json << 'EOF'
{
  "name": "react-demo",
  "private": true,
  "version": "0.0.0",
  "type": "module",
  "scripts": {
    "dev": "vite --host 0.0.0.0"
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

# Copy package.json to workspace
nexus workspace exec "$WORKSPACE_NAME" -- sh -c 'cat > /workspace/package.json' < /tmp/package.json

echo -e "\nStep 3: Installing dependencies..."
nexus workspace exec "$WORKSPACE_NAME" -- npm install

echo -e "\nStep 4: Creating sample App.jsx..."
cat > /tmp/App.jsx << 'EOF'
function App() {
  return (
    <div style={{ padding: '2rem', fontFamily: 'sans-serif' }}>
      <h1>🚀 React on Nexus</h1>
      <p>Your development workspace is ready!</p>
    </div>
  )
}
export default App
EOF

nexus workspace exec "$WORKSPACE_NAME" -- sh -c 'cat > /workspace/src/App.jsx' < /tmp/App.jsx

echo -e "\nStep 5: Creating main.jsx..."
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

nexus workspace exec "$WORKSPACE_NAME" -- sh -c 'cat > /workspace/src/main.jsx' < /tmp/main.jsx

echo -e "\nStep 6: Creating index.html..."
cat > /tmp/index.html << 'EOF'
<!DOCTYPE html>
<html lang="en">
  <head>
    <meta charset="UTF-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1.0" />
    <title>Nexus React App</title>
  </head>
  <body>
    <div id="root"></div>
    <script type="module" src="/src/main.jsx"></script>
  </body>
</html>
EOF

nexus workspace exec "$WORKSPACE_NAME" -- sh -c 'cat > /workspace/index.html' < /tmp/index.html

echo -e "\n✅ Workspace ready!"
echo "Start dev server: nexus workspace exec $WORKSPACE_NAME -- npm run dev"
echo "Add port forward: nexus workspace port add $WORKSPACE_NAME 5173"
echo "Then open: http://localhost:5173"
