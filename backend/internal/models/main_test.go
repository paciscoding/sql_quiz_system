package models

import (
	"testing"

	"github.com/paciscoding/sql-quiz-system/internal/publicid"
)

// TestMain initializes the publicid codec once for the whole models test
// binary. Several typed id fields (UserID, ExamID, ...) delegate their
// MarshalJSON/UnmarshalJSON to publicid.Encode/Decode; any test that
// marshals/unmarshals such a field would otherwise panic with "publicid:
// Encode called before Init" (regression-tester ARCH-013 item 2).
func TestMain(m *testing.M) {
	if err := publicid.Init("test-fixed-salt"); err != nil {
		panic("models: TestMain publicid.Init failed: " + err.Error())
	}
	m.Run()
}
