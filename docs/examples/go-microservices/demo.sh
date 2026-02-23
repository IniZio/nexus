#!/bin/bash
set -e

echo "🚀 Go Microservices Workspace Demo"
echo "==================================="

WORKSPACE_NAME="${1:-go-microservices}"

echo "Step 1: Creating workspace with DinD..."
nexus workspace create "$WORKSPACE_NAME" --dind

echo -e "\nStep 2: Setting up Go workspace..."

# Copy go.mod
nexus workspace exec "$WORKSPACE_NAME" -- sh -c 'cat > /workspace/go.mod' < go.mod

# Copy main Dockerfile
nexus workspace exec "$WORKSPACE_NAME" -- sh -c 'cat > /workspace/Dockerfile' < Dockerfile

# Copy docker-compose
nexus workspace exec "$WORKSPACE_NAME" -- sh -c 'cat > /workspace/docker-compose.yml' < docker-compose.yml

echo -e "\nStep 3: Creating service directories..."
nexus workspace exec "$WORKSPACE_NAME" -- mkdir -p /workspace/services/api-gateway
nexus workspace exec "$WORKSPACE_NAME" -- mkdir -p /workspace/services/auth-service
nexus workspace exec "$WORKSPACE_NAME" -- mkdir -p /workspace/services/user-service

echo -e "\nStep 4: Creating API Gateway..."
cat > /tmp/gateway.go << 'EOF'
package main

import (
    "fmt"
    "net/http"
    "os"
)

func main() {
    http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
        fmt.Fprintf(w, "API Gateway OK")
    })
    
    http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
        fmt.Fprintf(w, "Go Microservices API Gateway\n")
    })
    
    port := os.Getenv("PORT")
    if port == "" {
        port = "8080"
    }
    
    fmt.Printf("API Gateway starting on port %s\n", port)
    http.ListenAndServe(":"+port, nil)
}
EOF

nexus workspace exec "$WORKSPACE_NAME" -- sh -c 'cat > /workspace/services/api-gateway/main.go' < /tmp/gateway.go

echo -e "\nStep 5: Creating Auth Service..."
cat > /tmp/auth.go << 'EOF'
package main

import (
    "fmt"
    "net/http"
    "os"
)

func main() {
    http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
        fmt.Fprintf(w, "Auth Service OK")
    })
    
    http.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
        fmt.Fprintf(w, "Login endpoint\n")
    })
    
    port := os.Getenv("PORT")
    if port == "" {
        port = "8081"
    }
    
    fmt.Printf("Auth Service starting on port %s\n", port)
    http.ListenAndServe(":"+port, nil)
}
EOF

nexus workspace exec "$WORKSPACE_NAME" -- sh -c 'cat > /workspace/services/auth-service/main.go' < /tmp/auth.go

echo -e "\nStep 6: Creating User Service..."
cat > /tmp/user.go << 'EOF'
package main

import (
    "fmt"
    "net/http"
    "os"
)

func main() {
    http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
        fmt.Fprintf(w, "User Service OK")
    })
    
    http.HandleFunc("/users", func(w http.ResponseWriter, r *http.Request) {
        fmt.Fprintf(w, "Users endpoint\n")
    })
    
    port := os.Getenv("PORT")
    if port == "" {
        port = "8082"
    }
    
    fmt.Printf("User Service starting on port %s\n", port)
    http.ListenAndServe(":"+port, nil)
}
EOF

nexus workspace exec "$WORKSPACE_NAME" -- sh -c 'cat > /workspace/services/user-service/main.go' < /tmp/user.go

echo -e "\n✅ Go microservices workspace ready!"
echo "Start services: nexus workspace ssh $WORKSPACE_NAME && docker-compose up -d"
echo "Add port: nexus workspace port add $WORKSPACE_NAME 8080"
echo "Test: curl http://localhost:8080/health"
