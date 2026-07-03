package services

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
)

// PasswordCryptoEnvKey is the environment variable holding the base64-encoded
// 32-byte AES-256 key used to encrypt password-event values at rest.
const PasswordCryptoEnvKey = "PASSWORD_LOG_KEY"

// PasswordCipher wraps an AES-256-GCM AEAD used to encrypt/decrypt the password
// values captured in the password-event changelog. The key never touches the
// database; it is supplied via the PASSWORD_LOG_KEY environment variable.
type PasswordCipher struct {
	aead cipher.AEAD
}

// NewPasswordCipher builds a cipher from a raw 32-byte key.
func NewPasswordCipher(key []byte) (*PasswordCipher, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("password key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &PasswordCipher{aead: aead}, nil
}

// NewPasswordCipherFromEnv loads the cipher from PASSWORD_LOG_KEY. It returns
// (nil, false) when the variable is unset or invalid so callers can run with
// event logging disabled while login stays unaffected. A warning is logged when
// the value is present but malformed.
func NewPasswordCipherFromEnv() (*PasswordCipher, bool) {
	raw := os.Getenv(PasswordCryptoEnvKey)
	if raw == "" {
		log.Printf("password_crypto: %s not set — password-event logging disabled", PasswordCryptoEnvKey)
		return nil, false
	}
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		log.Printf("password_crypto: %s is not valid base64 — password-event logging disabled: %v", PasswordCryptoEnvKey, err)
		return nil, false
	}
	c, err := NewPasswordCipher(key)
	if err != nil {
		log.Printf("password_crypto: %s is invalid — password-event logging disabled: %v", PasswordCryptoEnvKey, err)
		return nil, false
	}
	return c, true
}

// Encrypt seals plaintext and returns the ciphertext plus the fresh 12-byte
// GCM nonce used. A distinct nonce is generated per call.
func (c *PasswordCipher) Encrypt(plaintext string) (ciphertext, nonce []byte, err error) {
	if c == nil {
		return nil, nil, errors.New("password cipher not configured")
	}
	nonce = make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, err
	}
	ciphertext = c.aead.Seal(nil, nonce, []byte(plaintext), nil)
	return ciphertext, nonce, nil
}

// Decrypt opens ciphertext with the supplied nonce. It fails if the key or
// nonce does not match the one used to encrypt.
func (c *PasswordCipher) Decrypt(ciphertext, nonce []byte) (string, error) {
	if c == nil {
		return "", errors.New("password cipher not configured")
	}
	plain, err := c.aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}
