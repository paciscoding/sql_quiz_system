package publicid

import (
	"testing"
)

// TestMain initializes the package-global codec once for the whole test
// binary with a fixed salt, so every test in this package gets deterministic,
// reproducible tokens (ARCH-004/ARCH-013).
func TestMain(m *testing.M) {
	if err := Init("test-fixed-salt"); err != nil {
		panic("publicid: TestMain Init failed: " + err.Error())
	}
	m.Run()
}

// usedTypes lists every Type actually adopted by the model layer (ARCH-015
// dropped TypeQuestion — exam-question ids stay plain int end-to-end, so it
// is intentionally excluded here).
var usedTypes = []Type{
	TypeUser,
	TypeExam,
	TypeCategory,
	TypePractice,
	TypeImage,
	TypeSubmission,
	TypeAttempt,
	TypeExamCategory,
}

func TestEncodeDecode_RoundTrip_AllUsedTypes(t *testing.T) {
	ids := []int{0, 1, 5, 42, 1000, 999999, 1 << 30}
	for _, ty := range usedTypes {
		ty := ty
		t.Run(string(rune(ty)), func(t *testing.T) {
			for _, id := range ids {
				token := Encode(ty, id)
				got, err := Decode(ty, token)
				if err != nil {
					t.Fatalf("Decode(%c, %q) unexpected error: %v", ty, token, err)
				}
				if got != id {
					t.Fatalf("round-trip mismatch: put %d, got %d (token %q)", id, got, token)
				}
			}
		})
	}
}

func TestEncodeDecode_ZeroID(t *testing.T) {
	token := Encode(TypeUser, 0)
	if len(token) < 2 {
		t.Fatalf("token too short: %q", token)
	}
	got, err := Decode(TypeUser, token)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got != 0 {
		t.Fatalf("want 0, got %d", got)
	}
}

func TestEncodeDecode_LargeID(t *testing.T) {
	const big = 1<<31 - 1 // max int32, comfortably large without overflow risk
	token := Encode(TypeExam, big)
	got, err := Decode(TypeExam, token)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got != big {
		t.Fatalf("want %d, got %d", big, got)
	}
}

func TestDecode_WrongTypeRejected(t *testing.T) {
	token := Encode(TypeUser, 5)
	if _, err := Decode(TypeExam, token); err == nil {
		t.Fatalf("expected error decoding a TypeUser token as TypeExam, got nil")
	}
	// Sanity: the same token DOES decode correctly under its real type.
	if got, err := Decode(TypeUser, token); err != nil || got != 5 {
		t.Fatalf("expected (5, nil) decoding under the correct type, got (%d, %v)", got, err)
	}
}

func TestDecode_TamperedTokenRejected(t *testing.T) {
	token := Encode(TypeCategory, 123)
	if len(token) < 3 {
		t.Fatalf("token too short to tamper meaningfully: %q", token)
	}
	body := []byte(token)
	// Flip one character in the sqids body (index 1, right after the type
	// prefix) to something different, preserving length.
	orig := body[1]
	for _, c := range []byte("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789") {
		if c != orig {
			body[1] = c
			break
		}
	}
	tampered := string(body)
	if tampered == token {
		t.Fatalf("failed to construct a tampered token distinct from the original")
	}
	if _, err := Decode(TypeCategory, tampered); err == nil {
		t.Fatalf("expected error decoding a single-char-tampered token, got nil")
	}
}

func TestDecode_TooShortRejected(t *testing.T) {
	cases := []string{"", "U"}
	for _, tc := range cases {
		if _, err := Decode(TypeUser, tc); err == nil {
			t.Fatalf("expected error decoding token %q (len<2), got nil", tc)
		}
	}
}

func TestDecode_GarbageRejected(t *testing.T) {
	// A syntactically-plausible but never-minted token should not decode.
	if _, err := Decode(TypeUser, "Uxxxxxxxxxx"); err == nil {
		t.Fatalf("expected error decoding garbage token, got nil")
	}
}

func TestInit_EmptySaltErrors(t *testing.T) {
	if err := Init(""); err == nil {
		t.Fatalf("expected Init(\"\") to return a non-nil error")
	}
	// Re-establish the fixed test codec so later tests in this binary (which
	// may run after this one, since Go doesn't guarantee ordering across
	// files but does run tests within a file top-to-bottom by declaration —
	// still, be defensive) keep working.
	if err := Init("test-fixed-salt"); err != nil {
		t.Fatalf("re-Init after empty-salt failure: %v", err)
	}
}

func TestInit_Deterministic_SameSaltSameToken(t *testing.T) {
	if err := Init("determinism-salt-A"); err != nil {
		t.Fatalf("Init: %v", err)
	}
	tokenA1 := Encode(TypeUser, 77)

	if err := Init("determinism-salt-A"); err != nil {
		t.Fatalf("Init: %v", err)
	}
	tokenA2 := Encode(TypeUser, 77)

	if tokenA1 != tokenA2 {
		t.Fatalf("same salt across two Init calls produced different tokens: %q vs %q", tokenA1, tokenA2)
	}

	// Restore the fixed test-wide salt for any subsequent tests.
	if err := Init("test-fixed-salt"); err != nil {
		t.Fatalf("restore Init: %v", err)
	}
}

func TestInit_DifferentSaltDifferentToken(t *testing.T) {
	if err := Init("salt-one"); err != nil {
		t.Fatalf("Init: %v", err)
	}
	tokenOne := Encode(TypeUser, 77)

	if err := Init("salt-two"); err != nil {
		t.Fatalf("Init: %v", err)
	}
	tokenTwo := Encode(TypeUser, 77)

	if tokenOne == tokenTwo {
		t.Fatalf("different salts produced the same token: %q", tokenOne)
	}

	// Restore the fixed test-wide salt for any subsequent tests.
	if err := Init("test-fixed-salt"); err != nil {
		t.Fatalf("restore Init: %v", err)
	}
}

func TestEncode_DifferentTypesSameID_DifferentTokens(t *testing.T) {
	a := Encode(TypeUser, 5)
	b := Encode(TypeExam, 5)
	if a == b {
		t.Fatalf("same id under two types produced identical tokens: %q", a)
	}
	// Different type prefixes at minimum.
	if a[0] == b[0] {
		t.Fatalf("expected distinct type-prefix bytes, both got %q", a[0])
	}
}
