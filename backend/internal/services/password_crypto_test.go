package services

import (
	"bytes"
	"crypto/rand"
	"testing"
)

func newTestCipher(t *testing.T) *PasswordCipher {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	c, err := NewPasswordCipher(key)
	if err != nil {
		t.Fatalf("NewPasswordCipher: %v", err)
	}
	return c
}

func TestPasswordCipher_RoundTrip(t *testing.T) {
	c := newTestCipher(t)
	plain := "Sup3rSecret-Pw!"
	ct, nonce, err := c.Encrypt(plain)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if len(nonce) != 12 {
		t.Fatalf("expected 12-byte nonce, got %d", len(nonce))
	}
	if bytes.Contains(ct, []byte(plain)) {
		t.Fatalf("ciphertext leaks plaintext")
	}
	got, err := c.Decrypt(ct, nonce)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if got != plain {
		t.Fatalf("round-trip mismatch: got %q want %q", got, plain)
	}
}

func TestPasswordCipher_WrongKeyFails(t *testing.T) {
	c1 := newTestCipher(t)
	key2 := make([]byte, 32)
	if _, err := rand.Read(key2); err != nil {
		t.Fatal(err)
	}
	c2, err := NewPasswordCipher(key2)
	if err != nil {
		t.Fatal(err)
	}
	ct, nonce, err := c1.Encrypt("hunter2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c2.Decrypt(ct, nonce); err == nil {
		t.Fatalf("expected decrypt with wrong key to fail")
	}
}

func TestPasswordCipher_DistinctNonces(t *testing.T) {
	c := newTestCipher(t)
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		_, nonce, err := c.Encrypt("same-plaintext")
		if err != nil {
			t.Fatal(err)
		}
		k := string(nonce)
		if seen[k] {
			t.Fatalf("nonce reused at iteration %d", i)
		}
		seen[k] = true
	}
}

func TestNewPasswordCipher_BadKeyLength(t *testing.T) {
	if _, err := NewPasswordCipher([]byte("too-short")); err == nil {
		t.Fatalf("expected error for non-32-byte key")
	}
}
