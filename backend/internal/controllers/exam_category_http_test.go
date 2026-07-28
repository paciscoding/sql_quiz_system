package controllers

// HTTP-integration tests for TODO-064 (ARCH-035 (2)/(3) names this file
// explicitly): ExamCategoryController's seven lecturer routes (ARCH-029) plus
// the student-facing category-headers endpoint (ARCH-030/TODO-060).
//
// This file builds its OWN gin.Engine (buildExamCategoryRouter), mirroring
// main.go's real route registration byte-for-byte, rather than importing
// cmd/main.go's wiring — a sibling parallel_group is editing cmd/main.go at
// the same time this file is written, so importing it here would be a race.
// Mirrors the harness pattern in bulk_reset_http_test.go /
// password_http_integration_test.go: openCtrlTestRepo(t) -> testsupport.SharedRepo,
// SKIPping cleanly without TEST_DATABASE_URL, mkCtrlUser/login/do/postJSON
// reused from password_http_integration_test.go / create_account_role_http_test.go
// in this package.
//
// Run: TEST_DATABASE_URL=postgres://... go test ./internal/controllers/... -run ExamCategory -v

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/paciscoding/sql-quiz-system/internal/models"
	"github.com/paciscoding/sql-quiz-system/internal/publicid"
	"github.com/paciscoding/sql-quiz-system/internal/repository"
	"github.com/paciscoding/sql-quiz-system/internal/services"
)

// buildExamCategoryRouter wires exactly /login + the eight exam-category
// routes with the real middleware stack, mirroring main.go's registration
// (quiz group's GetExamCategories at :242, staff group's seven routes at
// :276-284).
func buildExamCategoryRouter(repo *repository.PostgresRepository, auth *services.AuthService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	userCtrl := NewUserController(auth, services.NewStudentImportService(repo))
	examCatCtrl := NewExamCategoryController(repo)

	v1 := r.Group("/api/v1")
	v1.POST("/login", userCtrl.Login)

	quiz := v1.Group("/quiz")
	quiz.Use(AuthMiddleware(auth))
	{
		quiz.GET("/exams/:id/categories", RoleMiddleware(models.RoleStudent, models.RoleAdmin, models.RoleLecturer), examCatCtrl.GetExamCategories)
	}

	staff := v1.Group("/")
	staff.Use(AuthMiddleware(auth))
	{
		staffRole := RoleMiddleware(models.RoleAdmin, models.RoleLecturer)
		staff.GET("/lecturer/exams/:id/categories", staffRole, examCatCtrl.ListCategories)
		staff.POST("/lecturer/exams/:id/categories", staffRole, examCatCtrl.CreateCategory)
		staff.PUT("/lecturer/exams/:id/categories/:categoryId", staffRole, examCatCtrl.RenameCategory)
		staff.DELETE("/lecturer/exams/:id/categories/:categoryId", staffRole, examCatCtrl.DeleteCategory)
		staff.POST("/lecturer/exams/:id/categories/:categoryId/import", staffRole, examCatCtrl.ImportCategorySchema)
		staff.POST("/lecturer/exams/:id/categories/:categoryId/import-preview", staffRole, examCatCtrl.ImportCategoryPreviewSchema)
		staff.PUT("/lecturer/exams/:id/categories/:categoryId/introduction", staffRole, examCatCtrl.SetCategoryIntroduction)
	}
	return r
}

// putJSON fires a PUT request with a JSON body — postJSON
// (create_account_role_http_test.go) is POST-only.
func putJSON(r *gin.Engine, path, token string, body interface{}) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPut, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// mkFreshExamAndCategory creates a lecturer, a fresh examination and a fresh
// exam category owning its own schema, returning both the opaque tokens (for
// building request paths) and the internal ids/repo objects (for direct repo
// assertions). Every test using this registers its own t.Cleanup(DeleteExamination).
func mkFreshExamAndCategory(t *testing.T, repo *repository.PostgresRepository, prefix string) (examToken, catToken string, examID int, cat *models.ExamCategory) {
	t.Helper()
	ctx := context.Background()
	uid, err := repo.CreateUser(ctx, models.User{
		Username: fmt.Sprintf("%s_author_%d", prefix, time.Now().UnixNano()),
		Password: "hash", Role: models.RoleLecturer,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	examID, err = repo.CreateExamination(ctx, models.Examination{
		Title: fmt.Sprintf("%s_exam_%d", prefix, time.Now().UnixNano()), CreatedBy: models.UserID(uid),
	})
	if err != nil {
		t.Fatalf("CreateExamination: %v", err)
	}
	cat, err = repo.CreateExamCategory(ctx, examID, fmt.Sprintf("%s_cat_%d", prefix, time.Now().UnixNano()), &uid)
	if err != nil {
		t.Fatalf("CreateExamCategory: %v", err)
	}
	examToken = publicid.Encode(publicid.TypeExam, examID)
	catToken = publicid.Encode(publicid.TypeExamCategory, int(cat.ID))
	return
}

// idTokenString round-trips a typed id's own MarshalJSON to recover its raw
// opaque token string (with the leading type-byte prefix intact).
func idTokenString(t *testing.T, v interface{}) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal id: %v", err)
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("unmarshal id token: %v", err)
	}
	return s
}

// assertNoForbiddenKeys recursively walks a JSON-decoded value (map/slice
// tree) and fails the test if any of the forbidden keys appears anywhere,
// at any nesting depth — the literal ARCH-030/TODO-060 "no schema_name /
// preview_schema_name key at any nesting level" requirement.
func assertNoForbiddenKeys(t *testing.T, v interface{}, forbidden ...string) {
	t.Helper()
	switch val := v.(type) {
	case map[string]interface{}:
		for _, k := range forbidden {
			if _, ok := val[k]; ok {
				t.Fatalf("SECURITY REGRESSION: forbidden key %q found in the response body: %+v", k, val)
			}
		}
		for _, vv := range val {
			assertNoForbiddenKeys(t, vv, forbidden...)
		}
	case []interface{}:
		for _, vv := range val {
			assertNoForbiddenKeys(t, vv, forbidden...)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// D1: CRUD round trip.
// ─────────────────────────────────────────────────────────────────────────────

func TestExamCategoryCRUD_HTTP(t *testing.T) {
	repo := openCtrlTestRepo(t)
	auth := services.NewAuthService(repo, "test-jwt-secret-least-32-characters-long")
	r := buildExamCategoryRouter(repo, auth)

	admin := mkCtrlUser(t, repo, models.RoleAdmin, "adminpw")
	adminToken := login(t, r, admin.Username, "adminpw")

	examID, err := repo.CreateExamination(context.Background(), models.Examination{
		Title: fmt.Sprintf("crud_http_exam_%d", time.Now().UnixNano()), CreatedBy: admin.ID,
	})
	if err != nil {
		t.Fatalf("CreateExamination: %v", err)
	}
	t.Cleanup(func() { _ = repo.DeleteExamination(context.Background(), examID) })
	examToken := publicid.Encode(publicid.TypeExam, examID)

	w := postJSON(r, "/api/v1/lecturer/exams/"+examToken+"/categories", adminToken, map[string]interface{}{"name": "Part A"})
	if w.Code != http.StatusCreated {
		t.Fatalf("create: want 201, got %d (%s)", w.Code, w.Body.String())
	}
	var created models.ExamCategory
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("unmarshal created category: %v (body=%s)", err, w.Body.String())
	}
	idToken := idTokenString(t, created.ID)
	if idToken[0] != 'X' {
		t.Fatalf("expected the opaque id to start with 'X' (TypeExamCategory), got %q", idToken)
	}
	wantSchema := fmt.Sprintf("exam_cat_%d", int(created.ID))
	if created.SchemaName != wantSchema {
		t.Fatalf("expected schema_name=%q, got %q", wantSchema, created.SchemaName)
	}
	if created.QuestionCount != 0 {
		t.Fatalf("expected question_count=0, got %d", created.QuestionCount)
	}

	w = do(r, http.MethodGet, "/api/v1/lecturer/exams/"+examToken+"/categories", adminToken)
	if w.Code != http.StatusOK {
		t.Fatalf("list: want 200, got %d (%s)", w.Code, w.Body.String())
	}
	var list []models.ExamCategory
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	found := false
	for _, c := range list {
		if c.ID == created.ID {
			found = true
			if c.QuestionCount != 0 {
				t.Fatalf("expected listed question_count=0, got %d", c.QuestionCount)
			}
		}
	}
	if !found {
		t.Fatalf("expected the created category in the list, got %+v", list)
	}

	w = putJSON(r, "/api/v1/lecturer/exams/"+examToken+"/categories/"+idToken, adminToken, map[string]interface{}{"name": "Part A Renamed"})
	if w.Code != http.StatusOK {
		t.Fatalf("rename: want 200, got %d (%s)", w.Code, w.Body.String())
	}
	w = do(r, http.MethodGet, "/api/v1/lecturer/exams/"+examToken+"/categories", adminToken)
	json.Unmarshal(w.Body.Bytes(), &list)
	for _, c := range list {
		if c.ID == created.ID && c.Name != "Part A Renamed" {
			t.Fatalf("expected renamed category, got name=%q", c.Name)
		}
	}

	w = putJSON(r, "/api/v1/lecturer/exams/"+examToken+"/categories/"+idToken+"/introduction", adminToken, map[string]interface{}{"introduction": "some intro"})
	if w.Code != http.StatusOK {
		t.Fatalf("set introduction: want 200, got %d (%s)", w.Code, w.Body.String())
	}

	w = do(r, http.MethodDelete, "/api/v1/lecturer/exams/"+examToken+"/categories/"+idToken, adminToken)
	if w.Code != http.StatusOK {
		t.Fatalf("delete: want 200, got %d (%s)", w.Code, w.Body.String())
	}
	w = do(r, http.MethodGet, "/api/v1/lecturer/exams/"+examToken+"/categories", adminToken)
	json.Unmarshal(w.Body.Bytes(), &list)
	for _, c := range list {
		if c.ID == created.ID {
			t.Fatalf("expected the category gone after delete, still listed: %+v", c)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// D2: ownership (cross-exam 404) and cross-type-token (400 invalid id).
// ─────────────────────────────────────────────────────────────────────────────

// TestExamCategoryOwnership_HTTP proves a category created under exam A
// returns 404 on every nested /lecturer/exams/<B>/categories/<catA> route.
func TestExamCategoryOwnership_HTTP(t *testing.T) {
	repo := openCtrlTestRepo(t)
	auth := services.NewAuthService(repo, "test-jwt-secret-least-32-characters-long")
	r := buildExamCategoryRouter(repo, auth)
	admin := mkCtrlUser(t, repo, models.RoleAdmin, "adminpw")
	adminToken := login(t, r, admin.Username, "adminpw")

	_, catAToken, _, _ := mkFreshExamAndCategory(t, repo, "own_a")
	examBID, err := repo.CreateExamination(context.Background(), models.Examination{
		Title: fmt.Sprintf("own_b_exam_%d", time.Now().UnixNano()), CreatedBy: admin.ID,
	})
	if err != nil {
		t.Fatalf("CreateExamination(B): %v", err)
	}
	t.Cleanup(func() { _ = repo.DeleteExamination(context.Background(), examBID) })
	examBToken := publicid.Encode(publicid.TypeExam, examBID)

	base := "/api/v1/lecturer/exams/" + examBToken + "/categories/" + catAToken
	cases := []struct {
		name   string
		method string
		path   string
		body   map[string]interface{}
	}{
		{"GET (via list, not nested)", http.MethodGet, "/api/v1/lecturer/exams/" + examBToken + "/categories", nil},
		{"PUT rename", http.MethodPut, base, map[string]interface{}{"name": "hijack"}},
		{"DELETE", http.MethodDelete, base, nil},
		{"POST import", http.MethodPost, base + "/import", map[string]interface{}{"sql": "CREATE TABLE t (id INTEGER PRIMARY KEY);"}},
		{"POST import-preview", http.MethodPost, base + "/import-preview", map[string]interface{}{"sql": "CREATE TABLE t (id INTEGER PRIMARY KEY);"}},
		{"PUT introduction", http.MethodPut, base + "/introduction", map[string]interface{}{"introduction": "hijack"}},
	}
	for _, c := range cases {
		if c.name == "GET (via list, not nested)" {
			continue // list is exam-scoped only, no categoryId param to cross-check
		}
		t.Run(c.name, func(t *testing.T) {
			var w *httptest.ResponseRecorder
			switch c.method {
			case http.MethodPut:
				w = putJSON(r, c.path, adminToken, c.body)
			case http.MethodPost:
				w = postJSON(r, c.path, adminToken, c.body)
			case http.MethodDelete:
				w = do(r, http.MethodDelete, c.path, adminToken)
			}
			if w.Code != http.StatusNotFound {
				t.Fatalf("expected 404 reaching exam A's category through exam B, got %d (%s)", w.Code, w.Body.String())
			}
		})
	}
}

// TestExamCategoryCrossTypeToken_400 proves an exam-category token used on a
// practice-category route (and vice versa) returns 400 invalid id. This needs
// no database at all — idParam rejects the token before any repo call, so a
// nil-backed controller is safe, exactly like idparam_boundary_test.go.
func TestExamCategoryCrossTypeToken_400(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("practice-category token on an exam-category route", func(t *testing.T) {
		examCatCtrl := NewExamCategoryController(nil)
		r := gin.New()
		r.PUT("/api/v1/lecturer/exams/:id/categories/:categoryId", examCatCtrl.RenameCategory)

		examToken := publicid.Encode(publicid.TypeExam, 1)
		practiceCategoryToken := publicid.Encode(publicid.TypeCategory, 1) // wrong type: 'C', not 'X'

		w := doPlain(r, http.MethodPut, "/api/v1/lecturer/exams/"+examToken+"/categories/"+practiceCategoryToken)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 invalid id for a TypeCategory token on an exam-category route, got %d (%s)", w.Code, w.Body.String())
		}
	})

	t.Run("exam-category token on a practice-category route", func(t *testing.T) {
		practiceCtrl := NewPracticeController(nil, nil, nil)
		r := gin.New()
		r.PUT("/api/v1/lecturer/practice/categories/:id", practiceCtrl.RenameCategory)

		examCategoryToken := publicid.Encode(publicid.TypeExamCategory, 1) // wrong type: 'X', not 'C'

		w := doPlain(r, http.MethodPut, "/api/v1/lecturer/practice/categories/"+examCategoryToken)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 invalid id for a TypeExamCategory token on a practice-category route, got %d (%s)", w.Code, w.Body.String())
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// D3: authz — 403 STUDENT, 401 unauthenticated, 2xx ADMIN/LECTURER, on all
// seven lecturer routes.
// ─────────────────────────────────────────────────────────────────────────────

func TestExamCategoryAuthz_HTTP(t *testing.T) {
	repo := openCtrlTestRepo(t)
	auth := services.NewAuthService(repo, "test-jwt-secret-least-32-characters-long")
	r := buildExamCategoryRouter(repo, auth)

	admin := mkCtrlUser(t, repo, models.RoleAdmin, "adminpw")
	lecturer := mkCtrlUser(t, repo, models.RoleLecturer, "lecpw")
	student := mkCtrlUser(t, repo, models.RoleStudent, "studpw")
	adminToken := login(t, r, admin.Username, "adminpw")
	lecToken := login(t, r, lecturer.Username, "lecpw")
	studentToken := login(t, r, student.Username, "studpw")

	type routeCase struct {
		name   string
		method string
		mkPath func(examToken, catToken string) string
		body   map[string]interface{}
	}
	routes := []routeCase{
		{"list", http.MethodGet, func(e, c string) string { return "/api/v1/lecturer/exams/" + e + "/categories" }, nil},
		{"create", http.MethodPost, func(e, c string) string { return "/api/v1/lecturer/exams/" + e + "/categories" }, map[string]interface{}{"name": "AuthzCat"}},
		{"rename", http.MethodPut, func(e, c string) string { return "/api/v1/lecturer/exams/" + e + "/categories/" + c }, map[string]interface{}{"name": "Renamed"}},
		{"delete", http.MethodDelete, func(e, c string) string { return "/api/v1/lecturer/exams/" + e + "/categories/" + c }, nil},
		{"import", http.MethodPost, func(e, c string) string { return "/api/v1/lecturer/exams/" + e + "/categories/" + c + "/import" }, map[string]interface{}{"sql": "CREATE TABLE t (id INTEGER PRIMARY KEY);"}},
		{"import-preview", http.MethodPost, func(e, c string) string {
			return "/api/v1/lecturer/exams/" + e + "/categories/" + c + "/import-preview"
		}, map[string]interface{}{"sql": "CREATE TABLE t (id INTEGER PRIMARY KEY);"}},
		{"introduction", http.MethodPut, func(e, c string) string { return "/api/v1/lecturer/exams/" + e + "/categories/" + c + "/introduction" }, map[string]interface{}{"introduction": "hi"}},
	}

	fire := func(method, path, token string, body map[string]interface{}) *httptest.ResponseRecorder {
		switch method {
		case http.MethodGet:
			return do(r, http.MethodGet, path, token)
		case http.MethodDelete:
			return do(r, http.MethodDelete, path, token)
		case http.MethodPut:
			return putJSON(r, path, token, body)
		default:
			return postJSON(r, path, token, body)
		}
	}

	for _, rt := range routes {
		t.Run(rt.name, func(t *testing.T) {
			// 403 STUDENT and 401 unauthenticated share one throwaway fixture
			// (role/auth checks run before the handler touches the repo).
			examToken, catToken, _, _ := mkFreshExamAndCategory(t, repo, "authz_"+rt.name+"_neg")
			if w := fire(rt.method, rt.mkPath(examToken, catToken), studentToken, rt.body); w.Code != http.StatusForbidden {
				t.Errorf("STUDENT %s %s: want 403, got %d (%s)", rt.method, rt.name, w.Code, w.Body.String())
			}
			if w := fire(rt.method, rt.mkPath(examToken, catToken), "", rt.body); w.Code != http.StatusUnauthorized {
				t.Errorf("unauthenticated %s %s: want 401, got %d (%s)", rt.method, rt.name, w.Code, w.Body.String())
			}

			// 2xx ADMIN and 2xx LECTURER each get their OWN fresh fixture, since
			// several of these routes mutate/consume state (delete, import
			// without confirm_replace).
			examTokenAdmin, catTokenAdmin, _, _ := mkFreshExamAndCategory(t, repo, "authz_"+rt.name+"_admin")
			if w := fire(rt.method, rt.mkPath(examTokenAdmin, catTokenAdmin), adminToken, rt.body); w.Code >= 300 {
				t.Errorf("ADMIN %s %s: want 2xx, got %d (%s)", rt.method, rt.name, w.Code, w.Body.String())
			}
			examTokenLec, catTokenLec, _, _ := mkFreshExamAndCategory(t, repo, "authz_"+rt.name+"_lec")
			if w := fire(rt.method, rt.mkPath(examTokenLec, catTokenLec), lecToken, rt.body); w.Code >= 300 {
				t.Errorf("LECTURER %s %s: want 2xx, got %d (%s)", rt.method, rt.name, w.Code, w.Body.String())
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// D4: import — creates exam_cat_<n>, stamps schema_name, 409 without
// confirm_replace, 409 ErrExamActive, 400 for a schema-qualified script.
// ─────────────────────────────────────────────────────────────────────────────

func TestExamCategoryImport_HTTP(t *testing.T) {
	repo := openCtrlTestRepo(t)
	auth := services.NewAuthService(repo, "test-jwt-secret-least-32-characters-long")
	r := buildExamCategoryRouter(repo, auth)
	admin := mkCtrlUser(t, repo, models.RoleAdmin, "adminpw")
	adminToken := login(t, r, admin.Username, "adminpw")

	t.Run("creates_and_stamps_then_409_then_confirm_replace", func(t *testing.T) {
		examToken, catToken, examID, cat := mkFreshExamAndCategory(t, repo, "import_ok")
		t.Cleanup(func() { _ = repo.DeleteExamination(context.Background(), examID) })

		w := postJSON(r, "/api/v1/lecturer/exams/"+examToken+"/categories/"+catToken+"/import", adminToken,
			map[string]interface{}{"sql": "CREATE TABLE t (id INTEGER PRIMARY KEY);"})
		if w.Code != http.StatusOK {
			t.Fatalf("first import: want 200, got %d (%s)", w.Code, w.Body.String())
		}
		got, err := repo.GetExamCategory(context.Background(), examID, int(cat.ID))
		if err != nil {
			t.Fatalf("GetExamCategory: %v", err)
		}
		wantSchema := fmt.Sprintf("exam_cat_%d", int(cat.ID))
		if got.SchemaName != wantSchema {
			t.Fatalf("expected schema_name stamped to %q, got %q", wantSchema, got.SchemaName)
		}

		w = postJSON(r, "/api/v1/lecturer/exams/"+examToken+"/categories/"+catToken+"/import", adminToken,
			map[string]interface{}{"sql": "CREATE TABLE t2 (id INTEGER PRIMARY KEY);"})
		if w.Code != http.StatusConflict {
			t.Fatalf("second import without confirm_replace: want 409, got %d (%s)", w.Code, w.Body.String())
		}

		w = postJSON(r, "/api/v1/lecturer/exams/"+examToken+"/categories/"+catToken+"/import?confirm_replace=true", adminToken,
			map[string]interface{}{"sql": "CREATE TABLE t2 (id INTEGER PRIMARY KEY);"})
		if w.Code != http.StatusOK {
			t.Fatalf("import with confirm_replace: want 200, got %d (%s)", w.Code, w.Body.String())
		}
	})

	t.Run("schema_qualified_script_400", func(t *testing.T) {
		examToken, catToken, examID, _ := mkFreshExamAndCategory(t, repo, "import_badsql")
		t.Cleanup(func() { _ = repo.DeleteExamination(context.Background(), examID) })

		w := postJSON(r, "/api/v1/lecturer/exams/"+examToken+"/categories/"+catToken+"/import", adminToken,
			map[string]interface{}{"sql": "INSERT INTO public.foo VALUES (1)"})
		if w.Code != http.StatusBadRequest {
			t.Fatalf("schema-qualified import: want 400, got %d (%s)", w.Code, w.Body.String())
		}
	})

	t.Run("exam_active_409", func(t *testing.T) {
		examToken, catToken, examID, _ := mkFreshExamAndCategory(t, repo, "import_active")
		t.Cleanup(func() { _ = repo.DeleteExamination(context.Background(), examID) })

		// Give the exam a captured question so SetExaminationActive's readiness
		// gate passes, then activate it (instant mode: scheduled_start_at=now(),
		// no scheduled_end_at, so ActiveNow=true immediately).
		if err := repo.ImportSchemaIntoExamCurriculum(context.Background(), examID,
			"CREATE TABLE t (id INTEGER PRIMARY KEY);", int(admin.ID), false); err != nil {
			t.Fatalf("ImportSchemaIntoExamCurriculum: %v", err)
		}
		eid := models.ExamID(examID)
		if _, err := repo.CreateQuestion(context.Background(), models.Question{
			ExamID: &eid, CorrectSQL: "SELECT * FROM t", Type: "QUERY", Points: 1,
			Requirement: "r", CreatedBy: admin.ID,
		}); err != nil {
			t.Fatalf("CreateQuestion: %v", err)
		}
		if err := repo.SetExaminationActive(context.Background(), examID, true, nil, nil); err != nil {
			t.Fatalf("SetExaminationActive: %v", err)
		}
		t.Cleanup(func() { _ = repo.SetExaminationActive(context.Background(), examID, false, nil, nil) })

		w := postJSON(r, "/api/v1/lecturer/exams/"+examToken+"/categories/"+catToken+"/import", adminToken,
			map[string]interface{}{"sql": "CREATE TABLE t (id INTEGER PRIMARY KEY);"})
		if w.Code != http.StatusConflict {
			t.Fatalf("import while exam active: want 409, got %d (%s)", w.Code, w.Body.String())
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// D5: sandbox invalidation after import-with-replace.
// ─────────────────────────────────────────────────────────────────────────────

func TestExamCategorySandboxInvalidation_HTTP(t *testing.T) {
	repo := openCtrlTestRepo(t)
	auth := services.NewAuthService(repo, "test-jwt-secret-least-32-characters-long")
	r := buildExamCategoryRouter(repo, auth)
	admin := mkCtrlUser(t, repo, models.RoleAdmin, "adminpw")
	adminToken := login(t, r, admin.Username, "adminpw")

	examToken, catToken, examID, cat := mkFreshExamAndCategory(t, repo, "sbxinv")
	t.Cleanup(func() { _ = repo.DeleteExamination(context.Background(), examID) })

	w := postJSON(r, "/api/v1/lecturer/exams/"+examToken+"/categories/"+catToken+"/import", adminToken,
		map[string]interface{}{"sql": "CREATE TABLE t (id INTEGER PRIMARY KEY, val TEXT NOT NULL); INSERT INTO t VALUES (1, 'old');"})
	if w.Code != http.StatusOK {
		t.Fatalf("initial import: want 200, got %d (%s)", w.Code, w.Body.String())
	}

	ctx := context.Background()
	eid := models.ExamID(examID)
	if _, err := repo.CreateQuestion(ctx, models.Question{
		ExamID: &eid, CategoryID: &cat.ID, TargetTable: "t",
		CorrectSQL: "UPDATE t SET val = val WHERE id = 1", Type: "DML", Points: 1,
		Requirement: "r", CreatedBy: admin.ID,
	}); err != nil {
		t.Fatalf("CreateQuestion: %v", err)
	}
	if err := repo.EnsureExamCategorySchemasReady(ctx, examID); err != nil {
		t.Fatalf("EnsureExamCategorySchemasReady (initial provision): %v", err)
	}
	sandboxSchema := repository.ExamCategorySandboxName(int(cat.ID))
	hasTables, err := repo.SchemaHasTables(ctx, sandboxSchema)
	if err != nil || !hasTables {
		t.Fatalf("test setup: expected sandbox provisioned before replace, hasTables=%v err=%v", hasTables, err)
	}

	w = postJSON(r, "/api/v1/lecturer/exams/"+examToken+"/categories/"+catToken+"/import?confirm_replace=true", adminToken,
		map[string]interface{}{"sql": "CREATE TABLE t (id INTEGER PRIMARY KEY, val TEXT NOT NULL); INSERT INTO t VALUES (1, 'new-only-in-replacement');"})
	if w.Code != http.StatusOK {
		t.Fatalf("replace import: want 200, got %d (%s)", w.Code, w.Body.String())
	}

	hasTables, err = repo.SchemaHasTables(ctx, sandboxSchema)
	if err != nil {
		t.Fatalf("SchemaHasTables (post-replace): %v", err)
	}
	if hasTables {
		t.Fatalf("expected the stale sandbox %q to be dropped by import-with-replace", sandboxSchema)
	}

	// "The next GetExamQuestions" is simulated by the same lazy-provisioning
	// call quiz_controller.GetExamQuestions makes (ARCH-027 (3)).
	if err := repo.EnsureExamCategorySchemasReady(ctx, examID); err != nil {
		t.Fatalf("EnsureExamCategorySchemasReady (re-provision): %v", err)
	}
	// PostgresRepository.pool is unexported (this file lives in package
	// controllers), so read the re-provisioned row via the repo's own exported
	// generic Query helper instead.
	rows, err := repo.Query(ctx, sandboxSchema, "SELECT val FROM t WHERE id = 1")
	if err != nil {
		t.Fatalf("read re-provisioned sandbox row: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected exactly 1 row in the re-provisioned sandbox, got %d: %+v", len(rows), rows)
	}
	if val, _ := rows[0]["val"].(string); val != "new-only-in-replacement" {
		t.Fatalf("expected the re-provisioned sandbox to clone from the NEW data, got val=%v", rows[0]["val"])
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// D6: decoy import parity_warning.
// ─────────────────────────────────────────────────────────────────────────────

func TestExamCategoryDecoyImportParity_HTTP(t *testing.T) {
	repo := openCtrlTestRepo(t)
	auth := services.NewAuthService(repo, "test-jwt-secret-least-32-characters-long")
	r := buildExamCategoryRouter(repo, auth)
	admin := mkCtrlUser(t, repo, models.RoleAdmin, "adminpw")
	adminToken := login(t, r, admin.Username, "adminpw")

	t.Run("matching_structure_empty_warning", func(t *testing.T) {
		examToken, catToken, examID, _ := mkFreshExamAndCategory(t, repo, "decoy_match")
		t.Cleanup(func() { _ = repo.DeleteExamination(context.Background(), examID) })

		if w := postJSON(r, "/api/v1/lecturer/exams/"+examToken+"/categories/"+catToken+"/import", adminToken,
			map[string]interface{}{"sql": "CREATE TABLE t1 (id INTEGER PRIMARY KEY);"}); w.Code != http.StatusOK {
			t.Fatalf("real import: want 200, got %d (%s)", w.Code, w.Body.String())
		}

		w := postJSON(r, "/api/v1/lecturer/exams/"+examToken+"/categories/"+catToken+"/import-preview", adminToken,
			map[string]interface{}{"sql": "CREATE TABLE t1 (id INTEGER PRIMARY KEY);"})
		if w.Code != http.StatusOK {
			t.Fatalf("decoy import: want 200, got %d (%s)", w.Code, w.Body.String())
		}
		var resp struct {
			ParityWarning string `json:"parity_warning"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if resp.ParityWarning != "" {
			t.Fatalf("expected empty parity_warning for a matching decoy, got %q", resp.ParityWarning)
		}
	})

	t.Run("mismatched_structure_nonempty_warning_still_succeeds", func(t *testing.T) {
		examToken, catToken, examID, _ := mkFreshExamAndCategory(t, repo, "decoy_mismatch")
		t.Cleanup(func() { _ = repo.DeleteExamination(context.Background(), examID) })

		if w := postJSON(r, "/api/v1/lecturer/exams/"+examToken+"/categories/"+catToken+"/import", adminToken,
			map[string]interface{}{"sql": "CREATE TABLE t1 (id INTEGER PRIMARY KEY); CREATE TABLE t2 (id INTEGER PRIMARY KEY);"}); w.Code != http.StatusOK {
			t.Fatalf("real import: want 200, got %d (%s)", w.Code, w.Body.String())
		}

		// Decoy deliberately omits t2 - a structural mismatch.
		w := postJSON(r, "/api/v1/lecturer/exams/"+examToken+"/categories/"+catToken+"/import-preview", adminToken,
			map[string]interface{}{"sql": "CREATE TABLE t1 (id INTEGER PRIMARY KEY);"})
		if w.Code != http.StatusOK {
			t.Fatalf("decoy import (mismatched): want 200 (import must still succeed), got %d (%s)", w.Code, w.Body.String())
		}
		var resp struct {
			ParityWarning string `json:"parity_warning"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if resp.ParityWarning == "" {
			t.Fatal("expected a non-empty parity_warning naming the offending table (t2)")
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// D7 / ARCH-036: delete semantics.
// ─────────────────────────────────────────────────────────────────────────────

func TestExamCategoryDelete_HTTP(t *testing.T) {
	repo := openCtrlTestRepo(t)
	auth := services.NewAuthService(repo, "test-jwt-secret-least-32-characters-long")
	r := buildExamCategoryRouter(repo, auth)
	admin := mkCtrlUser(t, repo, models.RoleAdmin, "adminpw")
	adminToken := login(t, r, admin.Username, "adminpw")

	t.Run("non_empty_409_with_count_row_survives_no_force_escape", func(t *testing.T) {
		examToken, catToken, examID, cat := mkFreshExamAndCategory(t, repo, "del_nonempty")
		t.Cleanup(func() { _ = repo.DeleteExamination(context.Background(), examID) })

		ctx := context.Background()
		eid := models.ExamID(examID)
		if _, err := repo.CreateQuestion(ctx, models.Question{
			ExamID: &eid, CategoryID: &cat.ID, CorrectSQL: "SELECT 1", Type: "QUERY",
			Points: 1, Requirement: "r", CreatedBy: admin.ID,
		}); err != nil {
			t.Fatalf("CreateQuestion: %v", err)
		}

		path := "/api/v1/lecturer/exams/" + examToken + "/categories/" + catToken
		w := do(r, http.MethodDelete, path, adminToken)
		if w.Code != http.StatusConflict {
			t.Fatalf("delete non-empty: want 409, got %d (%s)", w.Code, w.Body.String())
		}
		var body struct {
			AttachedQuestionCount int `json:"attached_question_count"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if body.AttachedQuestionCount != 1 {
			t.Fatalf("expected attached_question_count=1, got %d (body=%s)", body.AttachedQuestionCount, w.Body.String())
		}

		if _, err := repo.GetExamCategory(ctx, examID, int(cat.ID)); err != nil {
			t.Fatalf("expected the category row to survive the refused delete, got %v", err)
		}

		// No force/detach escape hatch of any kind on this route.
		w = do(r, http.MethodDelete, path+"?force=true", adminToken)
		if w.Code != http.StatusConflict {
			t.Fatalf("ARCH-036 VIOLATED: expected ?force=true to have NO effect (still 409), got %d (%s)", w.Code, w.Body.String())
		}
		if _, err := repo.GetExamCategory(ctx, examID, int(cat.ID)); err != nil {
			t.Fatalf("expected the category row to still survive after a force=true attempt, got %v", err)
		}
	})

	t.Run("empty_200_and_schema_dropped", func(t *testing.T) {
		examToken, catToken, examID, cat := mkFreshExamAndCategory(t, repo, "del_empty")
		t.Cleanup(func() { _ = repo.DeleteExamination(context.Background(), examID) })

		path := "/api/v1/lecturer/exams/" + examToken + "/categories/" + catToken
		w := do(r, http.MethodDelete, path, adminToken)
		if w.Code != http.StatusOK {
			t.Fatalf("delete empty: want 200, got %d (%s)", w.Code, w.Body.String())
		}
		if _, err := repo.GetExamCategory(context.Background(), examID, int(cat.ID)); err == nil {
			t.Fatal("expected the category gone after a successful delete")
		}
	})

	t.Run("migrated_general_public_left_intact", func(t *testing.T) {
		uid, err := repo.CreateUser(context.Background(), models.User{
			Username: fmt.Sprintf("del_general_author_%d", time.Now().UnixNano()), Password: "hash", Role: models.RoleLecturer,
		})
		if err != nil {
			t.Fatalf("CreateUser: %v", err)
		}
		examID, err := repo.CreateExamination(context.Background(), models.Examination{
			Title: fmt.Sprintf("del_general_exam_%d", time.Now().UnixNano()), CreatedBy: models.UserID(uid),
		})
		if err != nil {
			t.Fatalf("CreateExamination: %v", err)
		}
		t.Cleanup(func() { _ = repo.DeleteExamination(context.Background(), examID) })

		// migrateExamCategories itself is unexported (package repository); this
		// file lives in package controllers, so trigger it the same way the real
		// boot path does: InitializeSystemTables calls it unconditionally and is
		// fully idempotent against an already-initialised database (openCtrlTestRepo
		// already ran it once for the whole test binary) — re-running it here
		// picks up this test's brand-new, still-category-less exam.
		if err := repo.InitializeSystemTables(context.Background()); err != nil {
			t.Fatalf("InitializeSystemTables (to trigger migrateExamCategories): %v", err)
		}
		cats, err := repo.ListExamCategories(context.Background(), examID)
		if err != nil || len(cats) != 1 || cats[0].SchemaName != "public" {
			t.Fatalf("test setup: expected exactly 1 legacy General with schema_name='public', got %+v (err=%v)", cats, err)
		}
		examToken := publicid.Encode(publicid.TypeExam, examID)
		catToken := publicid.Encode(publicid.TypeExamCategory, int(cats[0].ID))

		w := do(r, http.MethodDelete, "/api/v1/lecturer/exams/"+examToken+"/categories/"+catToken, adminToken)
		if w.Code != http.StatusOK {
			t.Fatalf("delete migrated empty General: want 200, got %d (%s)", w.Code, w.Body.String())
		}
		rows, err := repo.Query(context.Background(), "public", "SELECT count(*) AS c FROM pg_namespace WHERE nspname = 'public'")
		if err != nil {
			t.Fatalf("check public schema exists: %v", err)
		}
		if len(rows) != 1 {
			t.Fatalf("expected exactly 1 row from the pg_namespace count query, got %+v", rows)
		}
		if fmt.Sprintf("%v", rows[0]["c"]) != "1" {
			t.Fatal("DATA-LOSS REGRESSION: schema 'public' was removed by deleting a migrated General category")
		}
	})
}

// TestExamCategoryDelete_HTTP_TOCTOU_ConcurrentAttach is the regression-tester
// (step [7]) HTTP-layer sibling of repository's
// TestDeleteExamCategory_ConcurrentAttach_NeverSilentlyDetaches (FIX-B). The
// fixer's own regression test drives repo.DeleteExamCategory directly as a
// bare Go call; nothing in this batch proved the SAME race stays closed when
// the request arrives through the real gin route + full middleware stack
// (JWT auth, role check, idParam token decoding, JSON encoding of the 409
// body) instead of a bare function call. Deterministically orders the race
// exactly like the repository-level test: an uncommitted INSERT (session A,
// a raw connection acquired straight from the pool) is opened and held BEFORE
// the HTTP DELETE (session B) is even issued, and the test polls pg_locks for
// a genuine waiter before committing A — never relying on goroutine-scheduler
// timing to land in the racy interleaving.
func TestExamCategoryDelete_HTTP_TOCTOU_ConcurrentAttach(t *testing.T) {
	repo := openCtrlTestRepo(t)
	auth := services.NewAuthService(repo, "test-jwt-secret-least-32-characters-long")
	r := buildExamCategoryRouter(repo, auth)
	admin := mkCtrlUser(t, repo, models.RoleAdmin, "adminpw")
	adminToken := login(t, r, admin.Username, "adminpw")

	examToken, catToken, examID, cat := mkFreshExamAndCategory(t, repo, "del_toctou_http")
	t.Cleanup(func() { _ = repo.DeleteExamination(context.Background(), examID) })
	ctx := context.Background()

	// Session A: hold an uncommitted INSERT referencing the category open on
	// its own dedicated backend connection. Postgres must hold a FOR KEY
	// SHARE lock on the referenced app.exam_categories row for as long as
	// this transaction stays open.
	attachConn, err := repo.Pool().Acquire(ctx)
	if err != nil {
		t.Fatalf("Acquire (attach session): %v", err)
	}
	defer attachConn.Release()
	attachPID := attachConn.Conn().PgConn().PID()

	attachTx, err := attachConn.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin (attach session): %v", err)
	}
	var newQID int
	if err := attachTx.QueryRow(ctx,
		`INSERT INTO app.questions (exam_id, category_id, target_table, correct_sql, question_type, points, requirement, created_by, capture_error)
		 VALUES ($1, $2, 't', 'SELECT 1', 'QUERY', 1, 'http race attach', $3, '') RETURNING id`,
		examID, int(cat.ID), int(admin.ID)).Scan(&newQID); err != nil {
		t.Fatalf("attach INSERT (uncommitted): %v", err)
	}

	// Session B: the real HTTP DELETE, in a goroutine, racing against A's
	// still-open attach.
	path := "/api/v1/lecturer/exams/" + examToken + "/categories/" + catToken
	deleteDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		deleteDone <- do(r, http.MethodDelete, path, adminToken)
	}()

	// Prove B is genuinely blocked on the row lock (not merely "hasn't run
	// yet") by polling pg_locks for a waiter that isn't A's own backend.
	deadline := time.Now().Add(5 * time.Second)
	blocked := false
	for time.Now().Before(deadline) {
		var count int
		if err := repo.Pool().QueryRow(ctx,
			`SELECT COUNT(*) FROM pg_locks WHERE granted = false AND pid <> $1`,
			int32(attachPID)).Scan(&count); err != nil {
			t.Fatalf("poll pg_locks: %v", err)
		}
		if count > 0 {
			blocked = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !blocked {
		t.Fatal("expected the HTTP DELETE's underlying FOR UPDATE to visibly block on the concurrent uncommitted attach (no waiting lock ever observed in pg_locks) — the row lock did not serialise the two sessions through the HTTP layer")
	}
	select {
	case early := <-deleteDone:
		t.Fatalf("HTTP DELETE returned (status=%d body=%s) before the concurrent attach committed — it did not actually wait on the row lock", early.Code, early.Body.String())
	default:
	}

	// Release the attach: the DELETE's FOR UPDATE can now proceed and must
	// observe the committed question.
	if err := attachTx.Commit(ctx); err != nil {
		t.Fatalf("attach commit: %v", err)
	}

	var w *httptest.ResponseRecorder
	select {
	case w = <-deleteDone:
	case <-time.After(5 * time.Second):
		t.Fatal("HTTP DELETE did not return after the concurrent attach committed")
	}

	if w.Code != http.StatusConflict {
		t.Fatalf("RACE VIOLATED AT THE HTTP LAYER: expected 409 once the delete's COUNT (inside the same tx as the row lock) observes the concurrently-committed attach, got %d (%s)", w.Code, w.Body.String())
	}
	var body struct {
		AttachedQuestionCount int `json:"attached_question_count"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal 409 body: %v", err)
	}
	if body.AttachedQuestionCount != 1 {
		t.Fatalf("expected attached_question_count=1 in the HTTP 409 body, got %d (body=%s)", body.AttachedQuestionCount, w.Body.String())
	}

	// The one outcome ARCH-036/FIX-B forbids: the delete "succeeds" and the
	// concurrently-attached question's category_id gets silently nulled.
	if _, err := repo.GetExamCategory(ctx, examID, int(cat.ID)); err != nil {
		t.Fatalf("expected the category to still exist after the HTTP-layer-refused delete, got %v", err)
	}
	q, err := repo.GetQuestion(ctx, newQID)
	if err != nil {
		t.Fatalf("GetQuestion(newQID): %v", err)
	}
	if q.CategoryID == nil || int(*q.CategoryID) != int(cat.ID) {
		t.Fatalf("TOCTOU REGRESSION AT THE HTTP LAYER: the concurrently-attached question's category_id is %v after the raced HTTP delete — a question was silently detached by a delete that never observed it", q.CategoryID)
	}

	if err := repo.DeleteQuestion(ctx, newQID); err != nil {
		t.Fatalf("cleanup DeleteQuestion: %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Student categories endpoint (ARCH-030/TODO-060).
// ─────────────────────────────────────────────────────────────────────────────

func TestExamCategoriesStudentEndpoint_HTTP(t *testing.T) {
	repo := openCtrlTestRepo(t)
	auth := services.NewAuthService(repo, "test-jwt-secret-least-32-characters-long")
	r := buildExamCategoryRouter(repo, auth)

	admin := mkCtrlUser(t, repo, models.RoleAdmin, "adminpw")
	lecturer := mkCtrlUser(t, repo, models.RoleLecturer, "lecpw")
	adminToken := login(t, r, admin.Username, "adminpw")
	lecToken := login(t, r, lecturer.Username, "lecpw")

	enrolled := mkCtrlUser(t, repo, models.RoleStudent, "enrolledpw")
	notEnrolled := mkCtrlUser(t, repo, models.RoleStudent, "notenrolledpw")
	enrolledToken := login(t, r, enrolled.Username, "enrolledpw")
	notEnrolledToken := login(t, r, notEnrolled.Username, "notenrolledpw")

	examToken, catToken, examID, _ := mkFreshExamAndCategory(t, repo, "student_cats")
	t.Cleanup(func() { _ = repo.DeleteExamination(context.Background(), examID) })
	_ = catToken
	if err := repo.AddStudentsToExam(context.Background(), examID, []int{int(enrolled.ID)}); err != nil {
		t.Fatalf("AddStudentsToExam: %v", err)
	}

	// Enrolled student, inactive exam -> 200.
	w := do(r, http.MethodGet, "/api/v1/quiz/exams/"+examToken+"/categories", enrolledToken)
	if w.Code != http.StatusOK {
		t.Fatalf("enrolled student: want 200, got %d (%s)", w.Code, w.Body.String())
	}
	var generic interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &generic); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	assertNoForbiddenKeys(t, generic, "schema_name", "preview_schema_name")

	// Not-enrolled student -> 403.
	w = do(r, http.MethodGet, "/api/v1/quiz/exams/"+examToken+"/categories", notEnrolledToken)
	if w.Code != http.StatusForbidden {
		t.Fatalf("not-enrolled student: want 403, got %d (%s)", w.Code, w.Body.String())
	}

	// Unknown exam -> 404.
	unknownToken := publicid.Encode(publicid.TypeExam, 99999999)
	w = do(r, http.MethodGet, "/api/v1/quiz/exams/"+unknownToken+"/categories", enrolledToken)
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown exam: want 404, got %d (%s)", w.Code, w.Body.String())
	}

	// Admin/Lecturer bypass the enrollment check entirely.
	w = do(r, http.MethodGet, "/api/v1/quiz/exams/"+examToken+"/categories", adminToken)
	if w.Code != http.StatusOK {
		t.Fatalf("admin bypass: want 200, got %d (%s)", w.Code, w.Body.String())
	}
	w = do(r, http.MethodGet, "/api/v1/quiz/exams/"+examToken+"/categories", lecToken)
	if w.Code != http.StatusOK {
		t.Fatalf("lecturer bypass: want 200, got %d (%s)", w.Code, w.Body.String())
	}
}
