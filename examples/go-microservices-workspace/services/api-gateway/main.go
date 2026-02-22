package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"nexus/shared/models"
)

var (
	authServiceURL string
	userServiceURL string
	jwtSecret      string
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	authServiceURL = os.Getenv("AUTH_SERVICE_URL")
	if authServiceURL == "" {
		authServiceURL = "http://localhost:8081"
	}

	userServiceURL = os.Getenv("USER_SERVICE_URL")
	if userServiceURL == "" {
		userServiceURL = "http://localhost:8082"
	}

	jwtSecret = os.Getenv("JWT_SECRET")
	if jwtSecret == "" {
		jwtSecret = "default-secret-change-in-production"
	}

	// Routes
	http.HandleFunc("/health", healthHandler)
	http.HandleFunc("/api/auth/", authProxyHandler)
	http.HandleFunc("/api/users", usersProxyHandler)
	http.HandleFunc("/api/users/", usersProxyHandler)
	http.HandleFunc("/api/me", meHandler)

	addr := fmt.Sprintf(":%s", port)
	log.Printf("API Gateway starting on %s", addr)
	log.Printf("Auth service: %s", authServiceURL)
	log.Printf("User service: %s", userServiceURL)
	log.Fatal(http.ListenAndServe(addr, nil))
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Check downstream services
	authHealthy := checkServiceHealth(authServiceURL)
	userHealthy := checkServiceHealth(userServiceURL)

	status := "healthy"
	code := http.StatusOK
	if !authHealthy || !userHealthy {
		status = "degraded"
		code = http.StatusServiceUnavailable
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":  status,
		"service": "api-gateway",
		"timestamp": time.Now().Format(time.RFC3339),
		"dependencies": map[string]bool{
			"auth-service": authHealthy,
			"user-service": userHealthy,
		},
	})
}

func authProxyHandler(w http.ResponseWriter, r *http.Request) {
	// Strip /api/auth/ prefix and forward to auth service
	path := strings.TrimPrefix(r.URL.Path, "/api/auth")
	targetURL := authServiceURL + path

	if r.URL.RawQuery != "" {
		targetURL += "?" + r.URL.RawQuery
	}

	proxyRequest(w, r, targetURL)
}

func usersProxyHandler(w http.ResponseWriter, r *http.Request) {
	// Protected endpoints - check JWT
	if r.Method != http.MethodGet {
		// For non-GET, require auth
		if !validateJWT(w, r) {
			return
		}
	}

	// Forward to user service
	targetURL := userServiceURL + r.URL.Path
	if r.URL.RawQuery != "" {
		targetURL += "?" + r.URL.RawQuery
	}

	proxyRequest(w, r, targetURL)
}

func meHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract and validate JWT
	tokenString := extractBearerToken(r)
	if tokenString == "" {
		respondWithError(w, "Authorization required", http.StatusUnauthorized)
		return
	}

	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		return []byte(jwtSecret), nil
	})

	if err != nil || !token.Valid {
		respondWithError(w, "Invalid token", http.StatusUnauthorized)
		return
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		respondWithError(w, "Invalid token claims", http.StatusUnauthorized)
		return
	}

	email, _ := claims["email"].(string)
	if email == "" {
		respondWithError(w, "Invalid token", http.StatusUnauthorized)
		return
	}

	// Fetch user from user service
	userURL := fmt.Sprintf("%s/users/by-email/%s", userServiceURL, url.QueryEscape(email))
	
	req, _ := http.NewRequest("GET", userURL, nil)
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		respondWithError(w, "Failed to fetch user", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(body)
}

func checkServiceHealth(url string) bool {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(url + "/health")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func proxyRequest(w http.ResponseWriter, r *http.Request, targetURL string) {
	// Read body if present
	var body []byte
	if r.Body != nil {
		body, _ = io.ReadAll(r.Body)
	}

	// Create new request
	req, err := http.NewRequest(r.Method, targetURL, bytes.NewReader(body))
	if err != nil {
		respondWithError(w, "Failed to create request", http.StatusInternalServerError)
		return
	}

	// Copy headers
	for key, values := range r.Header {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}

	// Execute request
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		respondWithError(w, "Service unavailable", http.StatusServiceUnavailable)
		return
	}
	defer resp.Body.Close()

	// Copy response
	respBody, _ := io.ReadAll(resp.Body)
	
	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	
	w.WriteHeader(resp.StatusCode)
	w.Write(respBody)
}

func validateJWT(w http.ResponseWriter, r *http.Request) bool {
	tokenString := extractBearerToken(r)
	if tokenString == "" {
		respondWithError(w, "Authorization required", http.StatusUnauthorized)
		return false
	}

	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		return []byte(jwtSecret), nil
	})

	if err != nil || !token.Valid {
		respondWithError(w, "Invalid token", http.StatusUnauthorized)
		return false
	}

	return true
}

func extractBearerToken(r *http.Request) string {
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return ""
	}

	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 || strings.ToLower(parts[0]) != "bearer" {
		return ""
	}

	return parts[1]
}

func respondWithError(w http.ResponseWriter, message string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(models.APIResponse{
		Success: false,
		Error:   message,
	})
}
