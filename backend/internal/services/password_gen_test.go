package services

import (
	"strings"
	"testing"
)

func TestGeneratePassword_Length(t *testing.T) {
	pw := GeneratePassword()
	if len(pw) != PasswordLength {
		t.Fatalf("expected length %d, got %d (%q)", PasswordLength, len(pw), pw)
	}
}

func TestGeneratePassword_Alphabet(t *testing.T) {
	// No ambiguous characters (0/O, 1/l/I) allowed in any class.
	for _, banned := range []rune{'0', 'O', '1', 'l', 'I', 'o'} {
		if strings.ContainsRune(PasswordAlphabet, banned) {
			t.Fatalf("alphabet must not contain ambiguous char %q", banned)
		}
	}
	for i := 0; i < 500; i++ {
		pw := GeneratePassword()
		for _, r := range pw {
			if !strings.ContainsRune(PasswordAlphabet, r) {
				t.Fatalf("password %q contains out-of-alphabet char %q", pw, r)
			}
		}
	}
}

// TestGeneratePassword_AllClassesPresent proves every generated password
// contains at least one character from each of the four classes.
func TestGeneratePassword_AllClassesPresent(t *testing.T) {
	classes := map[string]string{
		"upper":   PasswordUpper,
		"lower":   PasswordLower,
		"digit":   PasswordDigits,
		"special": PasswordSpecial,
	}
	for i := 0; i < 500; i++ {
		pw := GeneratePassword()
		for name, class := range classes {
			if !strings.ContainsAny(pw, class) {
				t.Fatalf("password %q missing a %s character", pw, name)
			}
		}
	}
}

func TestGeneratePassword_Uniqueness(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		pw := GeneratePassword()
		if seen[pw] {
			t.Fatalf("duplicate password generated: %q", pw)
		}
		seen[pw] = true
	}
}

// TestGeneratePassword_Shuffled is a statistical guard on the Fisher-Yates
// shuffle: the class-guaranteed characters must not sit at fixed positions.
// Without the shuffle, positions 0-3 would always be upper, lower, digit,
// special in that exact order; with it, special characters spread across all
// 14 positions.
func TestGeneratePassword_Shuffled(t *testing.T) {
	const n = 300
	specialPositions := map[int]bool{}
	unshuffledPrefix := 0
	for i := 0; i < n; i++ {
		pw := GeneratePassword()
		for pos, r := range pw {
			if strings.ContainsRune(PasswordSpecial, r) {
				specialPositions[pos] = true
			}
		}
		if strings.ContainsRune(PasswordUpper, rune(pw[0])) &&
			strings.ContainsRune(PasswordLower, rune(pw[1])) &&
			strings.ContainsRune(PasswordDigits, rune(pw[2])) &&
			strings.ContainsRune(PasswordSpecial, rune(pw[3])) {
			unshuffledPrefix++
		}
	}
	// With a working shuffle, special chars appear at many distinct positions
	// across 300 samples (expected: all 14). Require a comfortable majority.
	if len(specialPositions) < 8 {
		t.Fatalf("special characters observed at only %d distinct positions; shuffle appears broken", len(specialPositions))
	}
	// The pre-shuffle class order must not survive on every sample. The pattern
	// can occur by chance occasionally, but never for all n samples.
	if unshuffledPrefix == n {
		t.Fatalf("every password kept the unshuffled class order upper/lower/digit/special")
	}
}

// TestGenerateResetOTP proves the one-time code is exactly ResetOTPLength
// decimal digits and is crypto-randomly varied (distinct values, full digit
// coverage across many samples).
func TestGenerateResetOTP(t *testing.T) {
	seen := map[string]bool{}
	digitsSeen := map[rune]bool{}
	for i := 0; i < 500; i++ {
		otp := GenerateResetOTP()
		if len(otp) != ResetOTPLength {
			t.Fatalf("expected %d digits, got %d (%q)", ResetOTPLength, len(otp), otp)
		}
		for _, r := range otp {
			if r < '0' || r > '9' {
				t.Fatalf("OTP %q contains non-digit %q", otp, r)
			}
			digitsSeen[r] = true
		}
		seen[otp] = true
	}
	// 500 draws from a space of 10^6: collisions are rare, and a constant or
	// low-entropy generator would produce far fewer distinct values.
	if len(seen) < 490 {
		t.Fatalf("expected ~500 distinct OTPs, got %d (insufficient randomness)", len(seen))
	}
	// 3000 digit draws: every digit 0-9 must appear.
	if len(digitsSeen) != 10 {
		t.Fatalf("expected all 10 digits across samples, saw %d", len(digitsSeen))
	}
}
