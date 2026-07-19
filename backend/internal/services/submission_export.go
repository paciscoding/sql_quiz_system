package services

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/csv"
	"fmt"
	"log"
	"strconv"
	"sync"
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
		"category_name", "question_requirement",
		"is_correct", "submitted_at", "sql_text", "validation_details"})
	for _, r := range rows {
		_ = w.Write([]string{
			strconv.Itoa(r.ID),
			strconv.Itoa(r.StudentID),
			r.Username,
			strconv.Itoa(r.PracticeQuestionID),
			r.CategoryName,
			r.QuestionRequirement,
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

// exportRepo is the minimal repository surface the exporter needs (satisfied by
// *repository.PostgresRepository). Defined here so the service is unit-testable
// with a fake.
type exportRepo interface {
	GetExportState(ctx context.Context) (models.ExportState, error)
	AdvanceExportWatermark(ctx context.Context, t time.Time) error
	ExportSubmissions(ctx context.Context, from, to time.Time) ([]models.SubmissionExportRow, error)
	ExportPracticeAttempts(ctx context.Context, from, to time.Time) ([]models.PracticeAttemptExportRow, error)
}

// exportSender is the email surface the exporter needs (satisfied by
// *SMTPEmailSender).
type exportSender interface {
	SendExport(to, subject, body string, attachments []ExportAttachment) error
}

// ExportConfig holds the runtime knobs resolved from the environment.
type ExportConfig struct {
	Interval  time.Duration
	Recipient string
}

// ExportResult reports the outcome of one export cycle (returned by the run-now
// endpoint).
type ExportResult struct {
	Enabled         bool      `json:"enabled"`
	WindowStart     time.Time `json:"window_start"`
	WindowEnd       time.Time `json:"window_end"`
	SubmissionCount int       `json:"submission_count"`
	PracticeCount   int       `json:"practice_count"`
	Sent            bool      `json:"sent"`
}

// SubmissionExportService packages the submission delta into gzip CSVs and emails
// them on an interval. ExportNow is serialized by a mutex so the ticker and a
// manual run-now cannot overlap.
type SubmissionExportService struct {
	repo   exportRepo
	sender exportSender
	cfg    ExportConfig
	mu     sync.Mutex
	now    func() time.Time
}

func NewSubmissionExportService(repo exportRepo, sender exportSender, cfg ExportConfig) *SubmissionExportService {
	return &SubmissionExportService{repo: repo, sender: sender, cfg: cfg, now: time.Now}
}

func (s *SubmissionExportService) Interval() time.Duration { return s.cfg.Interval }
func (s *SubmissionExportService) Recipient() string       { return s.cfg.Recipient }

// ExportNow runs one export cycle. See the watermark rules in the design spec:
// advance only on send success or a confirmed-empty window; hold on send failure.
func (s *SubmissionExportService) ExportNow(ctx context.Context) (ExportResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	st, err := s.repo.GetExportState(ctx)
	if err != nil {
		return ExportResult{}, err
	}
	if !st.Enabled {
		return ExportResult{Enabled: false}, nil
	}

	start := st.LastExportAt
	// windowEnd is app wall-clock; the delta assumes submission INSERTs are short
	// single-statement commits, so a row with submitted_at <= windowEnd committing
	// after this cycle's SELECT snapshot is not expected in practice.
	end := s.now()
	res := ExportResult{Enabled: true, WindowStart: start, WindowEnd: end}

	subs, err := s.repo.ExportSubmissions(ctx, start, end)
	if err != nil {
		return res, err
	}
	pracs, err := s.repo.ExportPracticeAttempts(ctx, start, end)
	if err != nil {
		return res, err
	}
	res.SubmissionCount = len(subs)
	res.PracticeCount = len(pracs)

	if len(subs) == 0 && len(pracs) == 0 {
		if err := s.repo.AdvanceExportWatermark(ctx, end); err != nil {
			return res, err
		}
		return res, nil
	}

	if s.cfg.Recipient == "" {
		return res, fmt.Errorf("submission-export: recipient not configured (set EXPORT_ADMIN_EMAIL or ADMIN_EMAIL); holding watermark")
	}

	stamp := end.UTC().Format("20060102")
	subGz, err := buildSubmissionsCSVGz(subs)
	if err != nil {
		return res, err
	}
	pracGz, err := buildPracticeAttemptsCSVGz(pracs)
	if err != nil {
		return res, err
	}
	atts := []ExportAttachment{
		{Filename: "submissions_" + stamp + ".csv.gz", Data: subGz, ContentType: "application/gzip"},
		{Filename: "practice_attempts_" + stamp + ".csv.gz", Data: pracGz, ContentType: "application/gzip"},
	}
	subject := "SQL Quiz System: submission log " + stamp
	body := fmt.Sprintf(
		"Submission-log export.\n\nWindow: %s  ->  %s\nExam submissions: %d\nPractice attempts: %d\n\nTwo gzip CSV files are attached.\n",
		start.UTC().Format(time.RFC3339), end.UTC().Format(time.RFC3339), len(subs), len(pracs))

	if err := s.sender.SendExport(s.cfg.Recipient, subject, body, atts); err != nil {
		return res, err // watermark intentionally NOT advanced
	}
	if err := s.repo.AdvanceExportWatermark(ctx, end); err != nil {
		return res, err
	}
	res.Sent = true
	return res, nil
}

// Run drives the export ticker until ctx is cancelled. ExportNow errors are
// logged and swallowed so the loop survives transient failures.
func (s *SubmissionExportService) Run(ctx context.Context) {
	t := time.NewTicker(s.cfg.Interval)
	defer t.Stop()
	log.Printf("submission-export: started (interval=%s recipient=%s)", s.cfg.Interval, s.cfg.Recipient)
	for {
		select {
		case <-ctx.Done():
			log.Printf("submission-export: stopped")
			return
		case <-t.C:
			res, err := s.ExportNow(ctx)
			switch {
			case err != nil:
				log.Printf("submission-export: cycle error: %v", err)
			case res.Sent:
				log.Printf("submission-export: sent (submissions=%d practice=%d)", res.SubmissionCount, res.PracticeCount)
			case res.Enabled:
				log.Printf("submission-export: nothing to send (empty window)")
			}
		}
	}
}
