module nexus/auth-service

go 1.21

require (
	github.com/golang-jwt/jwt/v5 v5.2.0
	github.com/lib/pq v1.10.9
	golang.org/x/crypto v0.18.0
	nexus/shared v0.0.0
)

replace nexus/shared = ../../shared
