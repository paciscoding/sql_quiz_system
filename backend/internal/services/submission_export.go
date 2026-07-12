package services

import (
	"bytes"
	"compress/gzip"
	"encoding/csv"
	"strconv"
	"time"

	"github.com/paciscoding/sql-quiz-system/internal/models"
)

// Env var names for the exporter.
const (
	ExportEnabledEnv   = "SUBMISSION_EXPORT_ENABLED"
	ExportIntervalEnv  = "SUBMISSION_EXPORT_INTERVAL"
	ExportRecipientEnv = "EXPORT_ADMIN_EMAIL"

	defaultExportInterval = 24 * time.Hour
)

// ParseExportInterval parses a Go duration; empty or invalid input falls back
// to 24h.
func ParseExportInterval(raw string) time.Duration {
	if raw == "" {
		return defaultExportInterval
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return defaultExportInterval
	}
	return d
}

func intPtrToStr(p *int) string {
	if p == nil {
		return ""
	}
	return strconv.Itoa(*p)
}

func gzipBytes(raw []byte) ([]byte, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(raw); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func buildSubmissionsCSVGz(rows []models.SubmissionExportRow) ([]byte, error) {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	_ = w.Write([]string{"id", "exam_id", "student_id", "username", "question_id",
		"is_correct", "score", "submitted_at", "sql_text", "validation_details"})
	for _, r := range rows {
		_ = w.Write([]string{
			strconv.Itoa(r.ID),
			intPtrToStr(r.ExamID),
			intPtrToStr(r.StudentID),
			r.Username,
			intPtrToStr(r.QuestionID),
			strconv.FormatBool(r.IsCorrect),
			strconv.FormatFloat(r.Score, 'f', -1, 64),
			r.SubmittedAt.Format(time.RFC3339),
			r.SQLText,
			r.ValidationDetails,
		})
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return nil, err
	}
	return gzipBytes(buf.Bytes())
}

func buildPracticeAttemptsCSVGz(rows []models.PracticeAttemptExportRow) ([]byte, error) {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	_ = w.Write([]string{"id", "student_id", "username", "practice_question_id",
		"is_correct", "submitted_at", "sql_text", "validation_details"})
	for _, r := range rows {
		_ = w.Write([]string{
			strconv.Itoa(r.ID),
			strconv.Itoa(r.StudentID),
			r.Username,
			strconv.Itoa(r.PracticeQuestionID),
			strconv.FormatBool(r.IsCorrect),
			r.SubmittedAt.Format(time.RFC3339),
			r.SQLText,
			r.ValidationDetails,
		})
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return nil, err
	}
	return gzipBytes(buf.Bytes())
}
