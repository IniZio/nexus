#!/bin/bash
set -e

echo "🐍 Python + Django Environment Demo"
echo "=================================="

ENV_NAME="${1:-django-demo}"

echo "Step 1: Creating environment..."
nexus environment create "$ENV_NAME"

echo -e "\nStep 2: Copying configuration files..."

# Copy requirements.txt
nexus environment exec "$ENV_NAME" -- sh -c 'cat > /workspace/requirements.txt' < requirements.txt

# Copy Dockerfile
nexus environment exec "$ENV_NAME" -- sh -c 'cat > /workspace/Dockerfile' < Dockerfile

# Copy docker-compose.yml
nexus environment exec "$ENV_NAME" -- sh -c 'cat > /workspace/docker-compose.yml' < docker-compose.yml

echo -e "\nStep 3: Installing dependencies..."
nexus environment exec "$ENV_NAME" -- pip install -r /workspace/requirements.txt

echo -e "\nStep 4: Creating Django project..."
nexus environment exec "$ENV_NAME" -- django-admin startproject myproject /workspace

echo -e "\nStep 5: Running initial migration..."
nexus environment exec "$ENV_NAME" -- python /workspace/manage.py migrate

echo -e "\n✅ Django environment ready!"
echo "Start server: nexus environment exec $ENV_NAME -- python manage.py runserver 0.0.0.0:8000"
echo ""
echo "Or start all services:"
echo "nexus environment ssh $ENV_NAME"
echo "docker-compose up -d"
