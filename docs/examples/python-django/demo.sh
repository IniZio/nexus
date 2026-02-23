#!/bin/bash
set -e

echo "🐍 Python + Django Workspace Demo"
echo "=================================="

WORKSPACE_NAME="${1:-django-demo}"

echo "Step 1: Creating workspace with DinD..."
nexus workspace create "$WORKSPACE_NAME" --dind

echo -e "\nStep 2: Copying configuration files..."

# Copy requirements.txt
nexus workspace exec "$WORKSPACE_NAME" -- sh -c 'cat > /workspace/requirements.txt' < requirements.txt

# Copy Dockerfile
nexus workspace exec "$WORKSPACE_NAME" -- sh -c 'cat > /workspace/Dockerfile' < Dockerfile

# Copy docker-compose.yml
nexus workspace exec "$WORKSPACE_NAME" -- sh -c 'cat > /workspace/docker-compose.yml' < docker-compose.yml

echo -e "\nStep 3: Installing dependencies..."
nexus workspace exec "$WORKSPACE_NAME" -- pip install -r /workspace/requirements.txt

echo -e "\nStep 4: Creating Django project..."
nexus workspace exec "$WORKSPACE_NAME" -- django-admin startproject myproject /workspace

echo -e "\nStep 5: Running initial migration..."
nexus workspace exec "$WORKSPACE_NAME" -- python /workspace/manage.py migrate

echo -e "\n✅ Django workspace ready!"
echo "Start server: nexus workspace exec $WORKSPACE_NAME -- python manage.py runserver 0.0.0.0:8000"
echo "Add port: nexus workspace port add $WORKSPACE_NAME 8000"
echo "URL: http://localhost:8000"
echo ""
echo "Or start all services:"
echo "nexus workspace ssh $WORKSPACE_NAME"
echo "docker-compose up -d"
