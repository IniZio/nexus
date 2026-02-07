#!/bin/bash
echo "Starting development environment..."

if ! command -v node &> /dev/null; then
    echo "Installing Node.js..."
    curl -fsSL https://deb.nodesource.com/setup_18.x | bash -
    apt-get install -y nodejs
fi

if ! command -v docker-compose &> /dev/null; then
    echo "Installing docker-compose..."
    apt-get update && apt-get install -y docker-compose
fi

echo "Starting services..."
docker-compose up -d postgres &
cd /workspace/server && npm install && HOST=0.0.0.0 PORT=5000 npm run dev &
API_PID=$!
sleep 5
cd /workspace/client && npm install && HOST=0.0.0.0 PORT=3000 npm run dev &
WEB_PID=$!

echo "Services starting... PIDs: API($API_PID), WEB($WEB_PID)"
echo "Development environment ready."
wait
