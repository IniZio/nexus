package coordination

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSQLiteRegistry_CreateAndRetrieve(t *testing.T) {
	dbFile := t.TempDir() + "/test.db"
	registry, err := NewSQLiteRegistry(dbFile)
	require.NoError(t, err)
	defer os.Remove(dbFile)

	installation := &GitHubInstallation{
		UserID:         "user123",
		GitHubUserID:   456,
		GitHubUsername: "testuser",
		Token:          "gho_test_token",
		TokenExpiresAt: time.Now().Add(time.Hour),
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}

	err = registry.StoreGitHubInstallation(installation)
	require.NoError(t, err)

	retrieved, err := registry.GetGitHubInstallation("user123")
	require.NoError(t, err)
	assert.Equal(t, installation.UserID, retrieved.UserID)
	assert.Equal(t, installation.GitHubUserID, retrieved.GitHubUserID)
	assert.Equal(t, installation.GitHubUsername, retrieved.GitHubUsername)
}

func TestSQLiteRegistry_StoreFork(t *testing.T) {
	dbFile := t.TempDir() + "/test.db"
	registry, err := NewSQLiteRegistry(dbFile)
	require.NoError(t, err)
	defer os.Remove(dbFile)

	fork := &GitHubFork{
		UserID:        "user123",
		OriginalOwner: "oursky",
		OriginalRepo:  "epson-eshop",
		ForkOwner:     "testuser",
		ForkURL:       "https://github.com/testuser/epson-eshop.git",
		CreatedAt:     time.Now(),
	}

	err = registry.StoreGitHubFork(fork)
	require.NoError(t, err)

	retrieved, err := registry.GetGitHubFork("user123", "oursky", "epson-eshop")
	require.NoError(t, err)
	assert.Equal(t, fork.ForkOwner, retrieved.ForkOwner)
	assert.Equal(t, fork.ForkURL, retrieved.ForkURL)
}

func TestSQLiteUserRegistry_RegisterAndRetrieve(t *testing.T) {
	dbFile := t.TempDir() + "/test.db"
	registry, err := NewSQLiteRegistry(dbFile)
	require.NoError(t, err)
	defer os.Remove(dbFile)

	userRegistry := registry.GetUserRegistry()

	user := &User{
		Username:  "testuser",
		PublicKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5...",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	err = userRegistry.Register(user)
	require.NoError(t, err)
	assert.NotEmpty(t, user.ID)

	retrieved, err := userRegistry.GetByUsername("testuser")
	require.NoError(t, err)
	assert.Equal(t, user.Username, retrieved.Username)
	assert.Equal(t, user.PublicKey, retrieved.PublicKey)
}

func TestSQLiteUserRegistry_ListUsers(t *testing.T) {
	dbFile := t.TempDir() + "/test.db"
	registry, err := NewSQLiteRegistry(dbFile)
	require.NoError(t, err)
	defer os.Remove(dbFile)

	userRegistry := registry.GetUserRegistry()

	user1 := &User{Username: "user1", PublicKey: "key1"}
	user2 := &User{Username: "user2", PublicKey: "key2"}

	err = userRegistry.Register(user1)
	require.NoError(t, err)

	err = userRegistry.Register(user2)
	require.NoError(t, err)

	users, err := userRegistry.List()
	require.NoError(t, err)
	assert.Len(t, users, 2)
}

func TestSQLiteRegistry_Persistence(t *testing.T) {
	dbFile := t.TempDir() + "/test.db"

	{
		registry, err := NewSQLiteRegistry(dbFile)
		require.NoError(t, err)

		installation := &GitHubInstallation{
			UserID:         "user123",
			GitHubUserID:   456,
			GitHubUsername: "testuser",
			Token:          "gho_test_token",
			TokenExpiresAt: time.Now().Add(time.Hour),
		}

		err = registry.StoreGitHubInstallation(installation)
		require.NoError(t, err)
	}

	{
		registry, err := NewSQLiteRegistry(dbFile)
		require.NoError(t, err)

		retrieved, err := registry.GetGitHubInstallation("user123")
		require.NoError(t, err)
		assert.Equal(t, "testuser", retrieved.GitHubUsername)
	}

	defer os.Remove(dbFile)
}

func TestSQLiteRegistry_DuplicateForkHandling(t *testing.T) {
	dbFile := t.TempDir() + "/test.db"
	registry, err := NewSQLiteRegistry(dbFile)
	require.NoError(t, err)
	defer os.Remove(dbFile)

	fork := &GitHubFork{
		UserID:        "user123",
		OriginalOwner: "oursky",
		OriginalRepo:  "epson-eshop",
		ForkOwner:     "testuser",
		ForkURL:       "https://github.com/testuser/epson-eshop.git",
	}

	err = registry.StoreGitHubFork(fork)
	require.NoError(t, err)

	err = registry.StoreGitHubFork(fork)
	require.NoError(t, err)

	retrieved, err := registry.GetGitHubFork("user123", "oursky", "epson-eshop")
	require.NoError(t, err)
	assert.Equal(t, fork.ForkOwner, retrieved.ForkOwner)
}
