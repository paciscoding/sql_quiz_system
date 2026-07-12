package repository

import (
	"context"
	"testing"
	"time"
)

func TestExportState_EnsureGetSetAdvance(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()

	// Reset to a known state for a deterministic test.
	if _, err := repo.pool.Exec(ctx, `DELETE FROM app.export_state`); err != nil {
		t.Fatalf("reset: %v", err)
	}

	// Ensure seeds exactly once with the given default.
	if err := repo.EnsureExportState(ctx, true); err != nil {
		t.Fatalf("EnsureExportState: %v", err)
	}
	// Second ensure with a different default must NOT overwrite.
	if err := repo.EnsureExportState(ctx, false); err != nil {
		t.Fatalf("EnsureExportState 2: %v", err)
	}

	st, err := repo.GetExportState(ctx)
	if err != nil {
		t.Fatalf("GetExportState: %v", err)
	}
	if !st.Enabled {
		t.Fatal("enabled should still be true after second ensure (no overwrite)")
	}

	if err := repo.SetExportEnabled(ctx, false); err != nil {
		t.Fatalf("SetExportEnabled: %v", err)
	}
	st, _ = repo.GetExportState(ctx)
	if st.Enabled {
		t.Fatal("enabled should be false after SetExportEnabled(false)")
	}

	mark := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	if err := repo.AdvanceExportWatermark(ctx, mark); err != nil {
		t.Fatalf("AdvanceExportWatermark: %v", err)
	}
	st, _ = repo.GetExportState(ctx)
	if !st.LastExportAt.Equal(mark) {
		t.Fatalf("watermark = %v, want %v", st.LastExportAt, mark)
	}
}
