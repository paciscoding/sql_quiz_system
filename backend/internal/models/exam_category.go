package models

import "time"

// ExamCategory is a first-class organizational unit for exam questions,
// mirroring PracticeCategory (practice.go). Each category owns its own SQL
// schema (named "exam_cat_<id>", see repository.ExamCategorySchemaName) into
// which a lecturer imports a database; every exam question in the category
// runs against that schema. Categories that were auto-adopted from a
// pre-existing exam's curriculum by the boot migration (migrateExamCategories,
// ARCH-026) instead share the exam's own schema (often literally "public")
// until a lecturer imports a database directly into them.
type ExamCategory struct {
	ID     ExamCategoryID `json:"id"`
	ExamID ExamID         `json:"exam_id"`
	Name   string         `json:"name"`
	// SchemaName is the category's grading schema. For a migrated category this
	// is a COPY of the exam's curriculum schema (e.g. "public" or
	// "curriculum_exam_7"); for a category that owns its schema it equals
	// ExamCategorySchemaName(ID) ("exam_cat_<id>").
	SchemaName string `json:"schema_name"`
	// PreviewSchemaName is the decoy schema (dual-schema grading) imported for
	// this category's student previews; empty when no decoy has been imported.
	PreviewSchemaName string `json:"preview_schema_name"`
	// HasPreviewSchema is a derived convenience flag: PreviewSchemaName != "".
	// Populated by the category read paths; false when no decoy exists.
	HasPreviewSchema bool   `json:"has_preview_schema"`
	Introduction     string `json:"introduction"`
	QuestionCount    int    `json:"question_count"`
	// CreatedBy is nullable: app.examinations.created_by is itself nullable and
	// the boot migration copies it verbatim onto the auto-adopted "General"
	// category.
	CreatedBy *UserID   `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// OwnsSchema is a derived convenience flag: SchemaName == the category's
	// own canonical exam_cat_<id> name (repository.ExamCategoryOwnsSchema).
	// It tells the UI whether this category still shares the exam-level
	// schema (a migrated/legacy-adopted category) or owns its own database.
	OwnsSchema bool `json:"owns_schema"`
}
