package main

import (
	"fmt"

	"github.com/nexus/nexus/pkg/auth"
)

func requireSession() (*auth.Session, error) {
	session, err := auth.LoadSession()
	if err != nil {
		return nil, fmt.Errorf("%w\n\nPlease run 'nexus login' first", err)
	}
	return session, nil
}

func getUserID() (string, error) {
	session, err := requireSession()
	if err != nil {
		return "", err
	}
	return session.UserID, nil
}
