package repository

import (
	"testing"

	"github.com/paciscoding/sql-quiz-system/internal/publicid"
)

// TestMain initializes the publicid codec once for the whole repository test
// binary. The pgx integration tests (postgres_typed_id_scan_test.go) JSON-
// encode scanned typed ids to verify they mint valid tokens; without this,
// that would panic with "publicid: Encode called before Init" (regression-
// tester ARCH-013 item 2). Harmless for the many DB-less tests in this
// package that never touch publicid.
func TestMain(m *testing.M) {
	if err := publicid.Init("test-fixed-salt"); err != nil {
		panic("repository: TestMain publicid.Init failed: " + err.Error())
	}
	m.Run()
}
