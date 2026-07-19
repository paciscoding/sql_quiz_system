// Package publicid encodes/decodes internal integer primary keys into
// opaque, type-prefixed public tokens so raw database ids are never
// exposed over the API. It wraps github.com/sqids/sqids-go with a
// deterministic alphabet shuffle derived from the ID_SALT secret.
//
// publicid is intentionally gin-free (stdlib + sqids-go only) so it stays
// pure and independently testable; any gin-aware helper (e.g. binding a
// path param to a decoded id) belongs in the controllers package instead.
package publicid

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math/rand"

	"github.com/sqids/sqids-go"
)

// Type identifies which kind of entity a token was minted for. Encoding the
// type as a one-byte prefix prevents replaying a token minted for one
// resource (e.g. an exam) against a route expecting another (e.g. a user).
type Type byte

const (
	TypeUser       Type = 'U'
	TypeExam       Type = 'E'
	TypeQuestion   Type = 'Q'
	TypeCategory   Type = 'C'
	TypePractice   Type = 'P'
	TypeImage      Type = 'I'
	TypeSubmission Type = 'B'
	TypeAttempt    Type = 'A'
)

const defaultAlphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// minLength is the minimum length of the sqids-generated body (excluding
// the one-byte type prefix), so the total token is at least 7 characters.
const minLength = 6

// codec holds the package-global *sqids.Sqids instance built by Init.
var codec *sqids.Sqids

// Init builds the package-global codec from salt by deterministically
// shuffling the default sqids alphabet. It returns an error if salt is
// empty (fail-fast: an empty salt would yield the unshuffled, predictable
// default alphabet) or if the underlying sqids library rejects the
// resulting options. Init may be called more than once; each call rebuilds
// the codec from scratch, leaving it in a valid state on success.
func Init(salt string) error {
	if salt == "" {
		return fmt.Errorf("publicid: Init requires a non-empty salt")
	}

	alphabet := shuffleAlphabet(defaultAlphabet, salt)

	s, err := sqids.New(sqids.Options{
		Alphabet:  alphabet,
		MinLength: minLength,
		Blocklist: []string{},
	})
	if err != nil {
		return fmt.Errorf("publicid: failed to build codec: %w", err)
	}

	codec = s
	return nil
}

// shuffleAlphabet deterministically permutes alphabet using a Fisher-Yates
// shuffle seeded from sha256(salt). The same salt always yields the same
// permutation; different salts yield different (unpredictable without the
// salt) permutations.
func shuffleAlphabet(alphabet, salt string) string {
	sum := sha256.Sum256([]byte(salt))
	seed := binary.BigEndian.Uint64(sum[:8])

	rng := rand.New(rand.NewSource(int64(seed)))

	runes := []rune(alphabet)
	for i := len(runes) - 1; i > 0; i-- {
		j := rng.Intn(i + 1)
		runes[i], runes[j] = runes[j], runes[i]
	}

	return string(runes)
}

// Encode returns the public token for id, prefixed with the one-byte
// identifier for t. It panics if id is negative or if Init has not been
// called successfully - both are programmer errors, not runtime
// conditions callers should need to handle.
func Encode(t Type, id int) string {
	if codec == nil {
		panic("publicid: Encode called before Init")
	}
	if id < 0 {
		panic("publicid: Encode called with negative id")
	}

	body, err := codec.Encode([]uint64{uint64(id)})
	if err != nil {
		// Only reachable via sqids internal invariants (e.g. exhausted
		// blocklist regeneration attempts); our Blocklist is always empty.
		panic(fmt.Sprintf("publicid: Encode failed: %v", err))
	}

	return string(byte(t)) + body
}

// Decode validates and decodes a public token, verifying that it was
// minted for the expected type t and has not been tampered with. It
// returns an error for: an empty or too-short token, a token minted for a
// different type, a token that does not decode to exactly one number, or a
// token that does not canonically re-encode to itself (which catches
// tampering, since sqids can decode multiple strings to the same number).
func Decode(t Type, token string) (int, error) {
	if codec == nil {
		panic("publicid: Decode called before Init")
	}

	if len(token) < 2 {
		return 0, fmt.Errorf("publicid: token too short")
	}

	if token[0] != byte(t) {
		return 0, fmt.Errorf("publicid: token type mismatch")
	}

	numbers := codec.Decode(token[1:])
	if len(numbers) != 1 {
		return 0, fmt.Errorf("publicid: token did not decode to exactly one id")
	}

	id := numbers[0]

	if Encode(t, int(id)) != token {
		return 0, fmt.Errorf("publicid: token failed canonical re-encode check")
	}

	return int(id), nil
}
