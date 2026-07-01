package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/paciscoding/sql-quiz-system/internal/models"
)

// introImageTable describes a family of intro-image operations.
// All fields MUST remain code-controlled constants — they are interpolated
// directly into SQL identifiers via fmt.Sprintf and must never come from
// user input.
type introImageTable struct {
	table     string // fully-qualified table name
	ownerCol  string // "category_id" or "examination_id"
	ownerNoun string // "category" or "examination", used in error messages
}

var (
	categoryIntroImages = introImageTable{"app.practice_category_intro_images", "category_id", "category"}
	examIntroImages     = introImageTable{"app.examination_intro_images", "examination_id", "examination"}
)

// ─── Private helpers ──────────────────────────────────────────────────────────

func (r *PostgresRepository) listIntroImages(ctx context.Context, t introImageTable, ownerID int) ([]models.IntroImage, error) {
	rows, err := r.pool.Query(ctx,
		fmt.Sprintf(`SELECT id, %s AS owner_id, sort_order, mime_type, byte_size, created_at, updated_at
		 FROM %s
		 WHERE %s = $1
		 ORDER BY sort_order, id`, t.ownerCol, t.table, t.ownerCol),
		ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	imgs := []models.IntroImage{}
	for rows.Next() {
		var img models.IntroImage
		if err := rows.Scan(&img.ID, &img.OwnerID, &img.SortOrder, &img.MimeType, &img.ByteSize,
			&img.CreatedAt, &img.UpdatedAt); err != nil {
			return nil, err
		}
		imgs = append(imgs, img)
	}
	return imgs, rows.Err()
}

func (r *PostgresRepository) getIntroImageBytes(ctx context.Context, t introImageTable, ownerID, imgID int) (mime string, data []byte, updatedAt time.Time, err error) {
	err = r.pool.QueryRow(ctx,
		fmt.Sprintf(`SELECT mime_type, image_bytes, updated_at
		 FROM %s
		 WHERE id = $1 AND %s = $2`, t.table, t.ownerCol),
		imgID, ownerID).Scan(&mime, &data, &updatedAt)
	return
}

func (r *PostgresRepository) insertIntroImage(ctx context.Context, t introImageTable, ownerID int, mime string, data []byte) (models.IntroImage, error) {
	var img models.IntroImage
	err := r.WithTransaction(ctx, func(tx pgx.Tx) error {
		var nextOrder int
		if err := tx.QueryRow(ctx,
			fmt.Sprintf(`SELECT COALESCE(MAX(sort_order), -1)+1
			 FROM %s
			 WHERE %s = $1`, t.table, t.ownerCol),
			ownerID).Scan(&nextOrder); err != nil {
			return err
		}
		return tx.QueryRow(ctx,
			fmt.Sprintf(`INSERT INTO %s
			   (%s, sort_order, mime_type, byte_size, image_bytes)
			 VALUES ($1, $2, $3, $4, $5)
			 RETURNING id, %s, sort_order, mime_type, byte_size, created_at, updated_at`,
				t.table, t.ownerCol, t.ownerCol),
			ownerID, nextOrder, mime, len(data), data).Scan(
			&img.ID, &img.OwnerID, &img.SortOrder, &img.MimeType, &img.ByteSize,
			&img.CreatedAt, &img.UpdatedAt)
	})
	return img, err
}

func (r *PostgresRepository) deleteIntroImage(ctx context.Context, t introImageTable, ownerID, imgID int) error {
	tag, err := r.pool.Exec(ctx,
		fmt.Sprintf(`DELETE FROM %s WHERE id = $1 AND %s = $2`, t.table, t.ownerCol),
		imgID, ownerID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

func (r *PostgresRepository) reorderIntroImages(ctx context.Context, t introImageTable, ownerID int, order []struct{ ID, SortOrder int }) error {
	return r.WithTransaction(ctx, func(tx pgx.Tx) error {
		for _, item := range order {
			tag, err := tx.Exec(ctx,
				fmt.Sprintf(`UPDATE %s
				 SET sort_order = $1, updated_at = now()
				 WHERE id = $2 AND %s = $3`, t.table, t.ownerCol),
				item.SortOrder, item.ID, ownerID)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return fmt.Errorf("image %d not found in %s %d", item.ID, t.ownerNoun, ownerID)
			}
		}
		return nil
	})
}

func (r *PostgresRepository) countIntroImageBytes(ctx context.Context, t introImageTable, ownerID int) (int64, error) {
	var total int64
	err := r.pool.QueryRow(ctx,
		fmt.Sprintf(`SELECT COALESCE(SUM(byte_size), 0)
		 FROM %s
		 WHERE %s = $1`, t.table, t.ownerCol),
		ownerID).Scan(&total)
	return total, err
}

// ─── Practice Category Intro Images ──────────────────────────────────────────

// ListCategoryIntroImages returns the metadata rows for all images attached to a
// practice category, ordered by (sort_order, id). image_bytes is excluded.
func (r *PostgresRepository) ListCategoryIntroImages(ctx context.Context, catID int) ([]models.IntroImage, error) {
	return r.listIntroImages(ctx, categoryIntroImages, catID)
}

// GetCategoryIntroImageBytes returns the raw bytes, mime type, and updated_at
// timestamp for a specific image belonging to catID. Returns pgx.ErrNoRows if
// the image does not exist or does not belong to catID.
func (r *PostgresRepository) GetCategoryIntroImageBytes(ctx context.Context, catID, imgID int) (mime string, data []byte, updatedAt time.Time, err error) {
	return r.getIntroImageBytes(ctx, categoryIntroImages, catID, imgID)
}

// InsertCategoryIntroImage appends a new image to a practice category. The
// sort_order is set to MAX(sort_order)+1 (or 0 for the first image) inside a
// single transaction. Returns the full metadata row on success.
func (r *PostgresRepository) InsertCategoryIntroImage(ctx context.Context, catID int, mime string, data []byte) (models.IntroImage, error) {
	return r.insertIntroImage(ctx, categoryIntroImages, catID, mime, data)
}

// DeleteCategoryIntroImage removes a single image by (id, category_id) so a
// wrong category scope cannot cross-delete. Returns pgx.ErrNoRows if not found.
func (r *PostgresRepository) DeleteCategoryIntroImage(ctx context.Context, catID, imgID int) error {
	return r.deleteIntroImage(ctx, categoryIntroImages, catID, imgID)
}

// ReorderCategoryIntroImages updates sort_order for each (id, sort_order) pair
// in order. All ids must belong to catID; the operation is rejected and rolled
// back if any id is foreign to the category.
func (r *PostgresRepository) ReorderCategoryIntroImages(ctx context.Context, catID int, order []struct{ ID, SortOrder int }) error {
	return r.reorderIntroImages(ctx, categoryIntroImages, catID, order)
}

// CountCategoryIntroImageBytes returns the total stored byte_size for all images
// in a practice category. Used for the 25 MB aggregate soft cap.
func (r *PostgresRepository) CountCategoryIntroImageBytes(ctx context.Context, catID int) (int64, error) {
	return r.countIntroImageBytes(ctx, categoryIntroImages, catID)
}

// ─── Examination Intro Images ─────────────────────────────────────────────────

// ListExamIntroImages returns the metadata rows for all images attached to an
// examination, ordered by (sort_order, id). image_bytes is excluded.
func (r *PostgresRepository) ListExamIntroImages(ctx context.Context, examID int) ([]models.IntroImage, error) {
	return r.listIntroImages(ctx, examIntroImages, examID)
}

// GetExamIntroImageBytes returns the raw bytes, mime type, and updated_at
// timestamp for a specific image belonging to examID. Returns pgx.ErrNoRows if
// the image does not exist or does not belong to examID.
func (r *PostgresRepository) GetExamIntroImageBytes(ctx context.Context, examID, imgID int) (mime string, data []byte, updatedAt time.Time, err error) {
	return r.getIntroImageBytes(ctx, examIntroImages, examID, imgID)
}

// InsertExamIntroImage appends a new image to an examination. sort_order is
// MAX(sort_order)+1 (or 0) inside a transaction. Returns full metadata on success.
func (r *PostgresRepository) InsertExamIntroImage(ctx context.Context, examID int, mime string, data []byte) (models.IntroImage, error) {
	return r.insertIntroImage(ctx, examIntroImages, examID, mime, data)
}

// DeleteExamIntroImage removes a single image by (id, examination_id) so a
// wrong exam scope cannot cross-delete. Returns pgx.ErrNoRows if not found.
func (r *PostgresRepository) DeleteExamIntroImage(ctx context.Context, examID, imgID int) error {
	return r.deleteIntroImage(ctx, examIntroImages, examID, imgID)
}

// ReorderExamIntroImages updates sort_order for each (id, sort_order) pair in
// order. All ids must belong to examID; rolled back on any foreign id.
func (r *PostgresRepository) ReorderExamIntroImages(ctx context.Context, examID int, order []struct{ ID, SortOrder int }) error {
	return r.reorderIntroImages(ctx, examIntroImages, examID, order)
}

// CountExamIntroImageBytes returns the total stored byte_size for all images
// in an examination. Used for the 25 MB aggregate soft cap.
func (r *PostgresRepository) CountExamIntroImageBytes(ctx context.Context, examID int) (int64, error) {
	return r.countIntroImageBytes(ctx, examIntroImages, examID)
}
