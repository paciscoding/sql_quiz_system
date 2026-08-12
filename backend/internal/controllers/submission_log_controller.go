package controllers

import (
	"encoding/csv"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/paciscoding/sql-quiz-system/internal/models"
	"github.com/paciscoding/sql-quiz-system/internal/publicid"
	"github.com/paciscoding/sql-quiz-system/internal/repository"
)

// SubmissionLogController serves the in-app, on-demand, filterable submission
// log for admins/lecturers: every attempt (exam or practice), never just the
// counted one. This is distinct from services/submission_export.go's emailed
// CSV.gz exporter — that watermark-driven job is untouched by this file.
//
// Read-only: no handler in this file writes to any table.
type SubmissionLogController struct {
	repo *repository.PostgresRepository
}

func NewSubmissionLogController(repo *repository.PostgresRepository) *SubmissionLogController {
	return &SubmissionLogController{repo: repo}
}

// The repository computes sql_preview (first 200 chars of sql_text,
// single-lined) directly in the list queries; the controller never truncates
// SQL text itself.
const (
	submissionLogDefaultLimit = 50
	submissionLogMaxLimit     = 200
)

// submissionLogQuery holds the parsed/validated query params shared by the
// list and CSV export handlers.
type submissionLogQuery struct {
	source      string // "exam" or "practice"
	examID      *int
	studentID   *int
	questionID  *int
	outcome     string // "all", "correct", "incorrect"
	countedOnly bool
	from        *time.Time
	to          *time.Time
	limit       int
	offset      int
}

// parseSubmissionLogQuery parses and validates every query param per the
// HTTP contract. On any invalid value it writes a 400 JSON response and
// returns ok=false; callers must return immediately in that case.
//
// withPaging controls whether limit/offset are parsed (the list endpoint
// paginates; the CSV export streams everything matching and ignores them).
func parseSubmissionLogQuery(c *gin.Context, withPaging bool) (submissionLogQuery, bool) {
	var q submissionLogQuery

	q.source = c.DefaultQuery("source", "exam")
	if q.source != "exam" && q.source != "practice" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "source must be 'exam' or 'practice'"})
		return q, false
	}

	q.outcome = c.DefaultQuery("outcome", "all")
	if q.outcome != "all" && q.outcome != "correct" && q.outcome != "incorrect" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "outcome must be 'all', 'correct', or 'incorrect'"})
		return q, false
	}

	if raw := c.Query("counted_only"); raw != "" {
		b, err := strconv.ParseBool(raw)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "counted_only must be a boolean"})
			return q, false
		}
		q.countedOnly = b
	}

	if raw := c.Query("exam_id"); raw != "" {
		id, err := publicid.Decode(publicid.TypeExam, raw)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid exam_id"})
			return q, false
		}
		q.examID = &id
	}

	if raw := c.Query("student_id"); raw != "" {
		id, err := publicid.Decode(publicid.TypeUser, raw)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid student_id"})
			return q, false
		}
		q.studentID = &id
	}

	if raw := c.Query("question_id"); raw != "" {
		id, err := strconv.Atoi(raw)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid question_id"})
			return q, false
		}
		q.questionID = &id
	}

	if raw := c.Query("from"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid from: must be RFC3339"})
			return q, false
		}
		q.from = &t
	}

	if raw := c.Query("to"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid to: must be RFC3339"})
			return q, false
		}
		q.to = &t
	}

	if q.from != nil && q.to != nil && q.from.After(*q.to) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "from must not be after to"})
		return q, false
	}

	if withPaging {
		q.limit = submissionLogDefaultLimit
		if raw := c.Query("limit"); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid limit"})
				return q, false
			}
			q.limit = n
		}
		if q.limit < 1 {
			q.limit = 1
		}
		if q.limit > submissionLogMaxLimit {
			q.limit = submissionLogMaxLimit
		}

		q.offset = 0
		if raw := c.Query("offset"); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid offset"})
				return q, false
			}
			q.offset = n
		}
		if q.offset < 0 {
			q.offset = 0
		}
	}

	return q, true
}

// toFilter converts the parsed query into the repository filter type.
func (q submissionLogQuery) toFilter() models.SubmissionLogFilter {
	return models.SubmissionLogFilter{
		ExamID:      q.examID,
		StudentID:   q.studentID,
		QuestionID:  q.questionID,
		Outcome:     q.outcome,
		CountedOnly: q.countedOnly,
		From:        q.from,
		To:          q.to,
		Limit:       q.limit,
		Offset:      q.offset,
	}
}

// ListSubmissionLog handles GET /api/v1/lecturer/submission-log
func (ctrl *SubmissionLogController) ListSubmissionLog(c *gin.Context) {
	q, ok := parseSubmissionLogQuery(c, true)
	if !ok {
		return
	}
	filter := q.toFilter()

	if q.source == "practice" {
		rows, total, err := ctrl.repo.ListPracticeSubmissionLog(c.Request.Context(), filter)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"rows": rows, "total": total, "limit": q.limit, "offset": q.offset})
		return
	}

	rows, total, err := ctrl.repo.ListExamSubmissionLog(c.Request.Context(), filter)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"rows": rows, "total": total, "limit": q.limit, "offset": q.offset})
}

// GetSubmissionLogEntry handles GET /api/v1/lecturer/submission-log/:submissionId
func (ctrl *SubmissionLogController) GetSubmissionLogEntry(c *gin.Context) {
	source := c.DefaultQuery("source", "exam")
	if source != "exam" && source != "practice" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "source must be 'exam' or 'practice'"})
		return
	}

	raw := c.Param("submissionId")

	if source == "practice" {
		id, err := publicid.Decode(publicid.TypeAttempt, raw)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid submissionId"})
			return
		}
		detail, err := ctrl.repo.GetPracticeAttemptDetail(c.Request.Context(), id)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, detail)
		return
	}

	id, err := publicid.Decode(publicid.TypeSubmission, raw)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid submissionId"})
		return
	}
	detail, err := ctrl.repo.GetExamSubmissionDetail(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, detail)
}

// ExportSubmissionLogCSV handles GET /api/v1/lecturer/submission-log.csv
// Streams straight to c.Writer via encoding/csv — the full result set is
// never buffered in memory. Once headers are written the HTTP status is
// locked in, so a mid-stream repository error cannot be reported as a
// non-200: it is logged server-side and surfaced to the client as a
// trailing CSV comment row instead of a silent truncation.
func (ctrl *SubmissionLogController) ExportSubmissionLogCSV(c *gin.Context) {
	q, ok := parseSubmissionLogQuery(c, false)
	if !ok {
		return
	}
	filter := q.toFilter()

	filename := fmt.Sprintf("submission-log-%s.csv", q.source)
	c.Header("Content-Type", "text/csv")
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	c.Status(http.StatusOK)

	w := csv.NewWriter(c.Writer)
	rowCount := 0
	flushEvery := 100

	maybeFlush := func() {
		rowCount++
		if rowCount%flushEvery == 0 {
			w.Flush()
			if f, ok := c.Writer.(interface{ Flush() }); ok {
				f.Flush()
			}
		}
	}

	// csvCell defuses spreadsheet formula injection. sql_text and the
	// requirement are free text a student or lecturer typed, and a cell opening
	// with = + - @ (or a tab/CR that Excel strips before parsing) is executed as
	// a formula by Excel and Sheets when the lecturer opens the download. The
	// leading apostrophe keeps the value readable while forcing it to be text.
	csvCell := func(s string) string {
		if s == "" {
			return s
		}
		switch s[0] {
		case '=', '+', '-', '@', '\t', '\r':
			return "'" + s
		}
		return s
	}

	writeTruncationNotice := func(streamErr error) {
		log.Printf("submission-log CSV export (source=%s) truncated mid-stream after %d rows: %v", q.source, rowCount, streamErr)
		_ = w.Write([]string{fmt.Sprintf("# EXPORT TRUNCATED: an error occurred after %d rows; data below this point is incomplete", rowCount)})
	}

	if q.source == "practice" {
		_ = w.Write([]string{"id", "student_id", "username", "student_number",
			"practice_question_id", "category_name", "requirement",
			"attempt_no", "is_counted", "is_correct", "submitted_at", "sql_text"})

		err := ctrl.repo.StreamPracticeSubmissionLog(c.Request.Context(), filter, func(d models.PracticeAttemptLogDetail) error {
			// Ids are emitted as the same opaque public ids the JSON endpoints
			// return. Writing int(d.ID) here would hand out raw sequential
			// primary keys — and, joined to username/student_number in the same
			// row, a raw-id-to-identity map for the whole system in one file.
			row := []string{
				publicid.Encode(publicid.TypeAttempt, int(d.ID)),
				publicid.Encode(publicid.TypeUser, int(d.StudentID)),
				d.Username,
				d.StudentNumber,
				publicid.Encode(publicid.TypePractice, int(d.PracticeQuestionID)),
				csvCell(d.CategoryName),
				csvCell(d.Requirement),
				strconv.Itoa(d.AttemptNo),
				strconv.FormatBool(d.IsCounted),
				strconv.FormatBool(d.IsCorrect),
				d.SubmittedAt.Format(time.RFC3339),
				csvCell(d.SQLText),
			}
			if werr := w.Write(row); werr != nil {
				return werr
			}
			maybeFlush()
			return nil
		})
		if err != nil {
			writeTruncationNotice(err)
		}
	} else {
		_ = w.Write([]string{"id", "exam_id", "exam_title", "student_id", "username", "student_number",
			"question_id", "question_display_no", "requirement", "question_type", "points",
			"attempt_no", "is_counted", "is_correct", "score", "submitted_at", "sql_text"})

		err := ctrl.repo.StreamExamSubmissionLog(c.Request.Context(), filter, func(d models.ExamSubmissionLogDetail) error {
			// Opaque ids, same reasoning as the practice branch above. The
			// question id stays a readable int — exam-question ids are not
			// encoded anywhere on the wire.
			row := []string{
				publicid.Encode(publicid.TypeSubmission, int(d.ID)),
				publicid.Encode(publicid.TypeExam, int(d.ExamID)),
				csvCell(d.ExamTitle),
				publicid.Encode(publicid.TypeUser, int(d.StudentID)),
				d.Username,
				d.StudentNumber,
				strconv.Itoa(d.QuestionID),
				strconv.Itoa(d.QuestionDisplayNo),
				csvCell(d.Requirement),
				d.QuestionType,
				strconv.FormatFloat(d.Points, 'f', -1, 64),
				strconv.Itoa(d.AttemptNo),
				strconv.FormatBool(d.IsCounted),
				strconv.FormatBool(d.IsCorrect),
				strconv.FormatFloat(d.Score, 'f', -1, 64),
				d.SubmittedAt.Format(time.RFC3339),
				csvCell(d.SQLText),
			}
			if werr := w.Write(row); werr != nil {
				return werr
			}
			maybeFlush()
			return nil
		})
		if err != nil {
			writeTruncationNotice(err)
		}
	}

	w.Flush()
}
