package repository

import (
	"context"
	"time"

	"github.com/paciscoding/sql-quiz-system/internal/models"
)

// EnsureExportState seeds the singleton export_state row on first boot. It is
// idempotent: once the row exists, the defaultEnabled argument is ignored, so a
// later admin UI toggle is never clobbered by the env-derived default.
func (r *PostgresRepository) EnsureExportState(ctx context.Context, defaultEnabled bool) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO app.export_state (id, enabled, last_export_at, updated_at)
		 VALUES (1, $1, now(), now())
		 ON CONFLICT (id) DO NOTHING`,
		defaultEnabled)
	return err
}

// GetExportState reads the singleton row.
func (r *PostgresRepository) GetExportState(ctx context.Context) (models.ExportState, error) {
	var st models.ExportState
	err := r.pool.QueryRow(ctx,
		`SELECT enabled, last_export_at, updated_at FROM app.export_state WHERE id = 1`).
		Scan(&st.Enabled, &st.LastExportAt, &st.UpdatedAt)
	return st, err
}

// SetExportEnabled flips the admin toggle and bumps updated_at.
func (r *PostgresRepository) SetExportEnabled(ctx context.Context, enabled bool) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE app.export_state SET enabled = $1, updated_at = now() WHERE id = 1`,
		enabled)
	return err
}

// AdvanceExportWatermark moves last_export_at forward after a successful (or
// confirmed-empty) export cycle.
func (r *PostgresRepository) AdvanceExportWatermark(ctx context.Context, t time.Time) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE app.export_state SET last_export_at = $1, updated_at = now() WHERE id = 1`,
		t)
	return err
}
