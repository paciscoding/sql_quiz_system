package controllers

import (
	"testing"

	"github.com/paciscoding/sql-quiz-system/internal/publicid"
)

// TestMain initializes the publicid codec once for the whole controllers
// test binary. The controllers package builds gin engines directly (not via
// cmd/main.go), so publicid.Init is never called by application startup code
// in-process here — without this, any test that JSON-encodes/decodes a typed
// model id (User.ID, Examination.ID, ...) would panic with "publicid: Encode
// called before Init" (regression-tester ARCH-013 item 2).
func TestMain(m *testing.M) {
	if err := publicid.Init("test-fixed-salt"); err != nil {
		panic("controllers: TestMain publicid.Init failed: " + err.Error())
	}
	m.Run()
}
