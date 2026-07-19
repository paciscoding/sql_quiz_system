package repository

import (
	"context"
	"errors"

	"github.com/paciscoding/sql-quiz-system/internal/models"
)

func (r *PostgresRepository) GetSubmissionsByExamID(ctx context.Context, examID int) ([]models.Submission, error) {
	query := `
		SELECT id, exam_id, student_id, question_id, sql_text, is_correct, score, validation_details, submitted_at
		FROM app.submissions WHERE exam_id = $1 ORDER BY submitted_at DESC`

	rows, err := r.pool.Query(ctx, query, examID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var submissions []models.Submission
	for rows.Next() {
		var s models.Submission
		err := rows.Scan(
			&s.ID, &s.ExamID, &s.StudentID, &s.QuestionID, &s.SQLText, &s.IsCorrect, &s.Score, &s.ValidationDetails, &s.SubmittedAt)
		if err != nil {
			return nil, err
		}
		submissions = append(submissions, s)
	}
	return submissions, rows.Err()
}

func (r *PostgresRepository) CreateSubmission(ctx context.Context, s models.Submission) error {
	query := `
		INSERT INTO app.submissions (exam_id, student_id, question_id, sql_text, is_correct, score, validation_details)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`

	_, err := r.pool.Exec(ctx, query, s.ExamID, s.StudentID, s.QuestionID, s.SQLText, s.IsCorrect, s.Score, s.ValidationDetails)
	return err
}

func (r *PostgresRepository) GetStudentExamResults(ctx context.Context, studentID, examID int) ([]models.StudentExamResult, error) {
	enrolled, err := r.IsStudentEnrolledInExam(ctx, studentID, examID)
	if err != nil {
		return nil, err
	}
	if !enrolled {
		return nil, errors.New("student not enrolled in exam")
	}

	query := `
		SELECT DISTINCT ON (s.question_id)
			s.question_id, q.requirement, q.question_type, q.points, s.sql_text, s.is_correct, s.score, s.submitted_at, s.validation_details
		FROM app.submissions s
		INNER JOIN app.questions q ON s.question_id = q.id
		WHERE s.student_id = $1 AND s.exam_id = $2
		ORDER BY s.question_id, s.score DESC, s.submitted_at DESC
	`
	rows, err := r.pool.Query(ctx, query, studentID, examID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []models.StudentExamResult
	for rows.Next() {
		var res models.StudentExamResult
		err := rows.Scan(
			&res.QuestionID, &res.Requirement, &res.Type, &res.Points, &res.SQLText, &res.IsCorrect, &res.Score, &res.SubmittedAt, &res.ValidationDetails)
		if err != nil {
			return nil, err
		}
		results = append(results, res)
	}
	return results, rows.Err()
}

func (r *PostgresRepository) GetStudentResultsSummary(ctx context.Context, studentID int) ([]models.ExamResultSummary, error) {
	query := `
WITH latest_submissions AS (
    SELECT DISTINCT ON (question_id)
        question_id, exam_id, student_id, is_correct, score
    FROM app.submissions
    WHERE student_id = $1
    ORDER BY question_id, score DESC, submitted_at DESC
),
exam_stats AS (
    SELECT
        e.id AS exam_id,
        e.title AS exam_title,
        e.is_active,
        e.created_at,
        COUNT(q.id) AS question_count,
        COALESCE(SUM(q.points), 0) AS max_score,
        COUNT(ls.question_id) AS answered_count,
        COUNT(ls.question_id) FILTER (WHERE ls.is_correct) AS correct_count,
        COALESCE(SUM(ls.score), 0) AS total_score
    FROM app.examinations e
    JOIN app.exam_students es ON e.id = es.exam_id
    LEFT JOIN app.questions q ON e.id = q.exam_id
    LEFT JOIN latest_submissions ls ON q.id = ls.question_id AND ls.student_id = $1
    WHERE es.student_id = $1
    GROUP BY e.id, e.title, e.is_active, e.created_at
)
SELECT
    exam_id, exam_title, is_active,
    question_count, answered_count, correct_count, total_score, max_score
FROM exam_stats
ORDER BY created_at DESC
	`
	rows, err := r.pool.Query(ctx, query, studentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var summaries []models.ExamResultSummary
	for rows.Next() {
		var s models.ExamResultSummary
		err := rows.Scan(
			&s.ExamID, &s.ExamTitle, &s.IsActive, &s.QuestionCount, &s.AnsweredCount, &s.CorrectCount, &s.TotalScore, &s.MaxScore)
		if err != nil {
			return nil, err
		}
		summaries = append(summaries, s)
	}
	return summaries, rows.Err()
}

func (r *PostgresRepository) GetConsolidatedScores(ctx context.Context) ([]models.ConsolidatedScore, error) {
	// 1. Fetch all students with their basic info
	queryStudents := `
		SELECT id, username, COALESCE(student_number, ''), COALESCE(first_name, ''),
		       COALESCE(last_name, ''), COALESCE(sec_lec, ''), COALESCE(sec_lab, '')
		FROM app.system_users
		WHERE role = 'STUDENT'
		ORDER BY student_number ASC, username ASC`

	rows, err := r.pool.Query(ctx, queryStudents)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	scoresMap := make(map[int]*models.ConsolidatedScore)
	var studentIDs []int

	for rows.Next() {
		var s models.ConsolidatedScore
		err := rows.Scan(&s.StudentID, &s.Username, &s.StudentNumber, &s.FirstName, &s.LastName, &s.SecLec, &s.SecLab)
		if err != nil {
			return nil, err
		}
		s.ExamScores = make(map[string]float64)
		scoresMap[int(s.StudentID)] = &s
		studentIDs = append(studentIDs, int(s.StudentID))
	}

	if len(studentIDs) == 0 {
		return []models.ConsolidatedScore{}, nil
	}

	// 2. Fetch exam scores (best attempt per question, summed per exam)
	queryExamScores := `
		WITH BestSubmissions AS (
			SELECT DISTINCT ON (student_id, question_id)
			       student_id, exam_id, score
			FROM app.submissions
			ORDER BY student_id, question_id, score DESC, submitted_at DESC
		)
		SELECT ls.student_id, e.title, SUM(ls.score)
		FROM BestSubmissions ls
		JOIN app.examinations e ON ls.exam_id = e.id
		GROUP BY ls.student_id, e.title`

	examRows, err := r.pool.Query(ctx, queryExamScores)
	if err != nil {
		return nil, err
	}
	defer examRows.Close()

	for examRows.Next() {
		var studentID int
		var examTitle string
		var totalScore float64
		if err := examRows.Scan(&studentID, &examTitle, &totalScore); err != nil {
			return nil, err
		}
		if s, ok := scoresMap[studentID]; ok {
			s.ExamScores[examTitle] = totalScore
			s.GrandTotal += totalScore
		}
	}

	// 3. Fetch practice scores (best attempt per question)
	queryPracticeScores := `
		SELECT student_id, SUM(points)
		FROM (
			SELECT DISTINCT ON (student_id, practice_question_id)
			       student_id, points
			FROM app.practice_attempts pa
			JOIN app.practice_questions pq ON pa.practice_question_id = pq.id
			WHERE pa.is_correct = true
			ORDER BY student_id, practice_question_id, submitted_at ASC
		) BestPractice
		GROUP BY student_id`

	practiceRows, err := r.pool.Query(ctx, queryPracticeScores)
	if err != nil {
		return nil, err
	}
	defer practiceRows.Close()

	for practiceRows.Next() {
		var studentID int
		var totalPoints float64
		if err := practiceRows.Scan(&studentID, &totalPoints); err != nil {
			return nil, err
		}
		if s, ok := scoresMap[studentID]; ok {
			s.PracticeTotal = totalPoints
			s.GrandTotal += totalPoints
		}
	}

	// Flatten map to slice
	result := make([]models.ConsolidatedScore, 0, len(scoresMap))
	for _, id := range studentIDs {
		result = append(result, *scoresMap[id])
	}
	return result, nil
}

// GetLecturerPracticeResults returns one row per student/question pair that
// has at least one practice attempt (no pagination, no rows for unattempted
// pairs), for the lecturer practice-results breakdown (ARCH-006).
func (r *PostgresRepository) GetLecturerPracticeResults(ctx context.Context) ([]models.LecturerPracticeResultRow, error) {
	query := `
		SELECT
			u.id, COALESCE(u.student_number, ''), u.username, COALESCE(u.first_name, ''),
			COALESCE(u.last_name, ''), COALESCE(u.sec_lec, ''), COALESCE(u.sec_lab, ''),
			pq.id, COALESCE(pq.category_id, 0), COALESCE(pc.name, ''),
			COALESCE(pq.title, ''), pq.requirement, pq.points,
			bool_or(pa.is_correct) as is_solved,
			count(pa.id) as attempt_count,
			CASE WHEN bool_or(pa.is_correct) THEN pq.points ELSE 0 END as earned_points
		FROM app.practice_attempts pa
		INNER JOIN app.system_users u ON u.id = pa.student_id
		INNER JOIN app.practice_questions pq ON pq.id = pa.practice_question_id
		LEFT JOIN app.practice_categories pc ON pc.id = pq.category_id
		GROUP BY u.id, u.student_number, u.username, u.first_name, u.last_name, u.sec_lec, u.sec_lab,
			pq.id, pq.category_id, pc.name, pq.title, pq.requirement, pq.points
		ORDER BY u.student_number ASC, u.username ASC, pc.name ASC, pq.id ASC`

	rows, err := r.pool.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []models.LecturerPracticeResultRow
	for rows.Next() {
		var row models.LecturerPracticeResultRow
		err := rows.Scan(
			&row.StudentID, &row.StudentNumber, &row.Username, &row.FirstName,
			&row.LastName, &row.SecLec, &row.SecLab,
			&row.QuestionID, &row.CategoryID, &row.CategoryName,
			&row.QuestionTitle, &row.Requirement, &row.Points,
			&row.IsSolved, &row.AttemptCount, &row.EarnedPoints)
		if err != nil {
			return nil, err
		}
		results = append(results, row)
	}
	return results, rows.Err()
}

func (r *PostgresRepository) GetPracticeSummary(ctx context.Context, studentID int) ([]models.PracticeSummary, error) {
	query := `
		SELECT
			pq.id, COALESCE(pq.category_id, 0), COALESCE(pc.name, pq.category),
			pq.category, pq.requirement, pq.points,
			COALESCE(bool_or(pa.is_correct), false) as is_solved,
			count(pa.id) as attempt_count
		FROM app.practice_questions pq
		LEFT JOIN app.practice_categories pc ON pc.id = pq.category_id
		LEFT JOIN app.practice_attempts pa ON pq.id = pa.practice_question_id AND pa.student_id = $1
		GROUP BY pq.id, pq.category_id, pc.name, pq.category, pq.requirement, pq.points
		ORDER BY COALESCE(pc.name, pq.category) ASC, pq.id ASC`

	rows, err := r.pool.Query(ctx, query, studentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var summaries []models.PracticeSummary
	for rows.Next() {
		var s models.PracticeSummary
		err := rows.Scan(&s.QuestionID, &s.CategoryID, &s.CategoryName, &s.Category, &s.Requirement, &s.Points, &s.IsSolved, &s.AttemptCount)
		if err != nil {
			return nil, err
		}
		summaries = append(summaries, s)
	}
	return summaries, rows.Err()
}
