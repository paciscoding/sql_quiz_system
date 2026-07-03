package services

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"math/big"
)

// Password character classes. Each generated password contains at least one
// character from every class. All classes keep the unambiguous principle: the
// visually confusable characters 0/O/o and 1/l/I are excluded so passwords are
// easy to read aloud and transcribe.
const (
	PasswordUpper   = "ABCDEFGHJKLMNPQRSTUVWXYZ"
	PasswordLower   = "abcdefghijkmnpqrstuvwxyz"
	PasswordDigits  = "23456789"
	PasswordSpecial = "!@#$%&*+-=?"
)

// PasswordAlphabet is the union of all password character classes.
const PasswordAlphabet = PasswordUpper + PasswordLower + PasswordDigits + PasswordSpecial

// PasswordLength is the number of characters in a generated password.
const PasswordLength = 14

// GeneratePassword returns a cryptographically-random 14-character password
// drawn from PasswordAlphabet, guaranteed to contain at least one uppercase
// letter, one lowercase letter, one digit, and one special character. It picks
// one character from each class, fills the remainder from the union, then
// shuffles with a crypto/rand Fisher-Yates so the guaranteed characters do not
// sit at predictable positions. It uses crypto/rand (never math/rand) so the
// output is unpredictable.
func GeneratePassword() string {
	out := make([]byte, 0, PasswordLength)
	// One guaranteed character per class.
	for _, class := range []string{PasswordUpper, PasswordLower, PasswordDigits, PasswordSpecial} {
		out = append(out, class[randIndex(len(class))])
	}
	// Fill the remaining positions from the full alphabet.
	for len(out) < PasswordLength {
		out = append(out, PasswordAlphabet[randIndex(len(PasswordAlphabet))])
	}
	// Fisher-Yates shuffle (crypto/rand) so class-guaranteed characters land at
	// uniformly random positions.
	for i := len(out) - 1; i > 0; i-- {
		j := randIndex(i + 1)
		out[i], out[j] = out[j], out[i]
	}
	return string(out)
}

// GenerateResetOTP returns a cryptographically-random numeric one-time code of
// ResetOTPLength digits (0-9) for the forgot-password verification step.
func GenerateResetOTP() string {
	out := make([]byte, ResetOTPLength)
	for i := range out {
		out[i] = byte('0' + randIndex(10))
	}
	return string(out)
}

// hashResetOTP returns the lowercase SHA-256 hex digest of a one-time code.
// Only this hash is ever persisted; the plaintext code exists solely in the
// email to the user.
func hashResetOTP(otp string) string {
	sum := sha256.Sum256([]byte(otp))
	return hex.EncodeToString(sum[:])
}

// randIndex returns a uniform random int in [0, n) from crypto/rand.
func randIndex(n int) int {
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		// crypto/rand failure is unrecoverable for credential generation.
		panic("password_gen: crypto/rand failed: " + err.Error())
	}
	return int(v.Int64())
}
