package vault

import (
	"crypto/rand"
	"fmt"

	"golang.org/x/crypto/chacha20poly1305"
)

// aad builds the Additional Authenticated Data from a Key.
// Binding both principal and integration prevents ciphertext swap between records.
func aad(k Key) []byte {
	return []byte(k.Principal + "\x00" + k.Integration)
}

func encryptRecord(plaintext, encKey []byte, k Key) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(encKey)
	if err != nil {
		return nil, fmt.Errorf("vault: new cipher: %w", err)
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("vault: rand nonce: %w", err)
	}
	return aead.Seal(nonce, nonce, plaintext, aad(k)), nil
}

func decryptRecord(ciphertext, encKey []byte, k Key) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(encKey)
	if err != nil {
		return nil, fmt.Errorf("vault: new cipher: %w", err)
	}
	ns := aead.NonceSize()
	if len(ciphertext) < ns {
		return nil, fmt.Errorf("vault: ciphertext too short")
	}
	pt, err := aead.Open(nil, ciphertext[:ns], ciphertext[ns:], aad(k))
	if err != nil {
		return nil, fmt.Errorf("vault: decrypt: %w", err)
	}
	return pt, nil
}
