package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/paciscoding/sql-quiz-system/internal/controllers"
	"github.com/paciscoding/sql-quiz-system/internal/middleware"
	"github.com/paciscoding/sql-quiz-system/internal/models"
	"github.com/paciscoding/sql-quiz-system/internal/publicid"
	"github.com/paciscoding/sql-quiz-system/internal/repository"
	"github.com/paciscoding/sql-quiz-system/internal/services"
)

func main() {
	// Database connection string from environment (required; no default).
	connString := os.Getenv("DATABASE_URL")
	if connString == "" {
		log.Fatal("DATABASE_URL is required")
	}

	// Public-id codec: derives the opaque-token alphabet from ID_SALT. Fail
	// fast on an empty/invalid salt rather than silently defaulting to the
	// unshuffled (predictable) alphabet - publicid.Init already rejects an
	// empty salt.
	if err := publicid.Init(os.Getenv("ID_SALT")); err != nil {
		log.Fatalf("Failed to initialize publicid codec: %v", err)
	}

	// Initialize Repository
	repo, err := repository.NewPostgresRepository(connString)
	if err != nil {
		log.Fatalf("Failed to initialize repository: %v", err)
	}
	defer repo.Close()

	// Initialize Submission Queue
	submissionQueue := services.NewSubmissionQueue(256, 30*time.Second)
	defer func() {
		shutCtx, shutCancel := context.WithTimeout(context.Background(), 35*time.Second)
		defer shutCancel()
		if err := submissionQueue.Shutdown(shutCtx); err != nil {
			log.Printf("Warning: submission queue shutdown: %v", err)
		}
	}()

	ctx := context.Background()

	// Setup restricted role for student queries
	if err := repo.CreateRestrictedUser(ctx); err != nil {
		log.Fatalf("Failed to setup restricted database role: %v", err)
	}

	// ARCH-022: provision the four restricted student-SQL LOGIN roles (one per
	// privilege class, each a non-superuser owning nothing) and build one pool
	// per class. Fail fast here, exactly like the ID_SALT check above — there
	// must be no path where a provisioning/connection failure silently leaves
	// student SQL executing on the privileged connection. Must run after
	// CreateRestrictedUser, which creates the sandbox roles these are members of.
	if err := repo.InitStudentPools(ctx); err != nil {
		log.Fatalf("Failed to initialize restricted student SQL pools: %v", err)
	}

	// Initialize System Tables
	if err := repo.InitializeSystemTables(ctx); err != nil {
		log.Fatalf("Failed to initialize system tables: %v", err)
	}

	// Initialize Practice Sandbox
	if err := repo.InitializePracticeSandbox(ctx); err != nil {
		log.Fatalf("Failed to initialize practice sandbox: %v", err)
	}

	// Initialize SSE hub for real-time exam status push
	hub := services.NewHub()
	defer hub.Close()

	// Initialize Services
	valService := services.NewValidationService(repo)
	studentImportService := services.NewStudentImportService(repo)

	jwtSecret := os.Getenv("JWT_SECRET")
	if len(jwtSecret) < 32 {
		log.Fatalf("JWT_SECRET must be at least 32 characters long")
	}
	authService := services.NewAuthService(repo, jwtSecret)

	// Email delivery: real Gmail SMTP when EMAIL_SENDER_ADDRESS is configured,
	// otherwise the no-op stub. The app password is never logged.
	var exportCtrl *controllers.ExportController
	if senderAddr := os.Getenv(services.EmailSenderAddressEnv); senderAddr != "" {
		smtpSender := services.NewSMTPEmailSender()
		authService.SetEmailSender(smtpSender)
		log.Printf("email: SMTP sender active (from=%s host=%s)", smtpSender.From(), smtpSender.Host())

		// Submission-log exporter: seed state, then run the interval loop.
		exportRecipient := os.Getenv(services.ExportRecipientEnv)
		if exportRecipient == "" {
			exportRecipient = os.Getenv("ADMIN_EMAIL")
		}
		exportEnabledDefault := true
		if v := os.Getenv(services.ExportEnabledEnv); v != "" {
			if parsed, perr := strconv.ParseBool(v); perr == nil {
				exportEnabledDefault = parsed
			}
		}
		if err := repo.EnsureExportState(ctx, exportEnabledDefault); err != nil {
			log.Printf("submission-export: seed state failed: %v", err)
		}
		exportCfg := services.ExportConfig{
			Interval:  services.ParseExportInterval(os.Getenv(services.ExportIntervalEnv)),
			Recipient: exportRecipient,
		}
		exportSvc := services.NewSubmissionExportService(repo, smtpSender, exportCfg)
		if exportRecipient == "" {
			log.Printf("submission-export: EXPORT_ADMIN_EMAIL/ADMIN_EMAIL unset; exporter loop not started (configure a recipient to enable)")
		} else {
			go exportSvc.Run(ctx)
		}
		exportCtrl = controllers.NewExportController(repo, exportSvc)
	} else {
		log.Printf("email: NoopEmailSender active (%s unset; emails are logged, not sent)", services.EmailSenderAddressEnv)
	}

	// Seed Admin on first startup. The password is server-generated (never set by
	// hand); it is emailed to ADMIN_EMAIL and also printed once below as a
	// no-lockout fallback. Idempotent — a prior boot's admin is left untouched, so
	// startup still succeeds when ADMIN_USER is unset or the admin already exists.
	adminUser := os.Getenv("ADMIN_USER")
	adminEmail := os.Getenv("ADMIN_EMAIL")
	if adminUser == "" {
		log.Printf("Warning: ADMIN_USER unset; skipping default admin seeding")
	} else {
		if adminEmail == "" {
			log.Printf("Warning: ADMIN_EMAIL unset; the generated admin password will only be printed below, not emailed")
		}
		if pw, err := authService.SeedAdminAccount(ctx, adminUser, adminEmail); err != nil {
			log.Printf("Warning: Failed to seed admin: %v", err)
		} else if pw != "" {
			// First seed only. This is the sole bootstrap channel if email is off
			// or SMTP fails, so log it once here.
			log.Printf("Seeded admin %q with generated password: %s", adminUser, pw)
		}
	}

	// Initialize Controllers
	quizCtrl := controllers.NewQuizController(valService, submissionQueue, repo, hub, authService)
	lecturerCtrl := controllers.NewLecturerController(repo, hub)
	lecturerCtrl.SetStudentImportService(studentImportService)
	lecturerCtrl.SetAuthService(authService)
	userCtrl := controllers.NewUserController(authService, studentImportService)
	practiceCtrl := controllers.NewPracticeController(repo, valService, authService)
	examCategoryCtrl := controllers.NewExamCategoryController(repo)
	// Practice is live during exams by default; this gate only blocks practice
	// when an active exam has its block_practice_during_exam toggle on (OQ-1).
	practiceFreezeMW := middleware.PracticeFreezeMiddleware(repo)

	// Setup Gin
	r := gin.Default()

	config := cors.DefaultConfig()
	config.AllowMethods = []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"}
	config.AllowHeaders = []string{"Origin", "Content-Type", "Accept", "Authorization", "X-Requested-With"}
	allowedOrigins := os.Getenv("ALLOWED_ORIGINS")
	if allowedOrigins != "" {
		config.AllowOrigins = []string{"*"}
	} else {
		config.AllowOrigins = []string{"http://localhost"}
	}
	r.Use(cors.New(config))

	// Healthcheck
	r.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "UP"})
	})

	// API Routes
	v1 := r.Group("/api/v1")
	{
		v1.POST("/login", userCtrl.Login)
		// Public forgot-password step 1: NO auth middleware. Always returns a generic 200.
		v1.POST("/forgot-password", userCtrl.ForgotPassword)
		// Public forgot-password step 2: NO auth middleware. Verifies the emailed
		// code; every failure returns the same generic 400.
		v1.POST("/forgot-password/verify", userCtrl.VerifyForgotPassword)

		// Protected User Routes
		user := v1.Group("/users")
		user.Use(controllers.AuthMiddleware(authService))
		{
			user.POST("/password", userCtrl.ChangePassword)
			user.GET("/students", controllers.RoleMiddleware(models.RoleAdmin, models.RoleLecturer), userCtrl.ListStudents)

			// Admin-only User Management
			adminUsers := user.Group("/")
			adminUsers.Use(controllers.RoleMiddleware(models.RoleAdmin))
			{
				adminUsers.GET("/all", userCtrl.GetAllUsers)
				adminUsers.PUT("/:id", userCtrl.UpdateUser)
				adminUsers.DELETE("/:id", userCtrl.DeleteUser)
				// Password management (admin only).
				adminUsers.POST("/:id/reset-password", userCtrl.ResetPassword)
				adminUsers.POST("/:id/resend-credential", userCtrl.ResendCredential)
				adminUsers.GET("/:id/password-events", userCtrl.GetPasswordEvents)
				// Bulk password reset (ARCH-031/ARCH-032): admin-only, student-only
				// targets, generated passwords, never emailed. A static sibling of the
				// /:id routes above — this group already mixes GET "/all" with PUT "/:id".
				adminUsers.POST("/bulk-reset-password", userCtrl.BulkResetPassword)
			}
		}

		// SSE stream route (authentication done inside handler via ?access_token= query param)
		// Must be registered OUTSIDE the quiz group to bypass the bearer-auth middleware.
		// Registered before /quiz/:id routes to avoid Gin catching the path in a wildcard.
		v1.GET("/quiz/exams/:id/stream", quizCtrl.StreamExamStatus)

		// Protected Quiz Routes
		quiz := v1.Group("/quiz")
		quiz.Use(controllers.AuthMiddleware(authService))
		{
			quiz.POST("/submit", controllers.RoleMiddleware(models.RoleStudent), quizCtrl.SubmitAnswer)
			quiz.POST("/preview", controllers.RoleMiddleware(models.RoleStudent), quizCtrl.PreviewAnswer)
			quiz.GET("/exams", controllers.RoleMiddleware(models.RoleStudent), quizCtrl.GetExams)
			quiz.GET("/exams/:id/questions", controllers.RoleMiddleware(models.RoleStudent), quizCtrl.GetExamQuestions)
			quiz.GET("/exams/:id/results", controllers.RoleMiddleware(models.RoleStudent), quizCtrl.GetExamResults)
			// Exam intro: enrollment-gated, NOT active-gated (first page after start + revisitable).
			quiz.GET("/exams/:id/introduction", controllers.RoleMiddleware(models.RoleStudent), quizCtrl.GetExamIntroduction)
			// Exam intro images: metadata list and binary stream. Staff (Admin/Lecturer)
			// also read these from IntroImagesEditor on the lecturer view; the handler
			// itself still enforces student enrollment when the caller is a student.
			quiz.GET("/exams/:id/intro-images", controllers.RoleMiddleware(models.RoleStudent, models.RoleAdmin, models.RoleLecturer), quizCtrl.ListExamIntroImages)
			quiz.GET("/exams/:id/intro-images/:imageId", controllers.RoleMiddleware(models.RoleStudent, models.RoleAdmin, models.RoleLecturer), quizCtrl.GetExamIntroImage)
			// Exam categories (read-only "category header" view): enrollment-gated,
			// NOT active-gated, same precedent as intro-images above; staff bypass
			// the enrollment check inside the handler.
			quiz.GET("/exams/:id/categories", controllers.RoleMiddleware(models.RoleStudent, models.RoleAdmin, models.RoleLecturer), examCategoryCtrl.GetExamCategories)
			quiz.GET("/results", controllers.RoleMiddleware(models.RoleStudent), quizCtrl.GetMyResultsSummary)
		}

		// Protected Lecturer/Admin Routes
		staff := v1.Group("/")
		staff.Use(controllers.AuthMiddleware(authService))
		{
			staff.POST("/lecturer/question", controllers.RoleMiddleware(models.RoleAdmin, models.RoleLecturer), lecturerCtrl.UpsertQuestion)
			staff.GET("/lecturer/exams/max-scores", controllers.RoleMiddleware(models.RoleAdmin, models.RoleLecturer), lecturerCtrl.GetExamMaxScores)
			staff.GET("/lecturer/exams/:id/questions", controllers.RoleMiddleware(models.RoleAdmin, models.RoleLecturer), lecturerCtrl.GetExamQuestions)
			staff.DELETE("/lecturer/questions/:id", controllers.RoleMiddleware(models.RoleAdmin, models.RoleLecturer), lecturerCtrl.DeleteQuestion)
			staff.POST("/lecturer/preview", controllers.RoleMiddleware(models.RoleAdmin, models.RoleLecturer), lecturerCtrl.PreviewCorrectSQL)
			staff.POST("/lecturer/import-schema", controllers.RoleMiddleware(models.RoleAdmin, models.RoleLecturer), lecturerCtrl.ImportSchema)
			staff.POST("/lecturer/students/import", controllers.RoleMiddleware(models.RoleAdmin, models.RoleLecturer), lecturerCtrl.ImportStudents)
			staff.GET("/lecturer/exams", controllers.RoleMiddleware(models.RoleAdmin, models.RoleLecturer), lecturerCtrl.ListExaminations)
			staff.POST("/lecturer/exams", controllers.RoleMiddleware(models.RoleAdmin, models.RoleLecturer), lecturerCtrl.CreateExamination)
			staff.GET("/lecturer/exams/:id/submissions", controllers.RoleMiddleware(models.RoleAdmin, models.RoleLecturer), lecturerCtrl.ListSubmissions)
			staff.PUT("/lecturer/exams/:id", controllers.RoleMiddleware(models.RoleAdmin, models.RoleLecturer), lecturerCtrl.RenameExamination)
			staff.DELETE("/lecturer/exams/:id", controllers.RoleMiddleware(models.RoleAdmin, models.RoleLecturer), lecturerCtrl.DeleteExamination)
			staff.PUT("/lecturer/exams/:id/activate", controllers.RoleMiddleware(models.RoleAdmin, models.RoleLecturer), lecturerCtrl.ActivateExamination)
			staff.PUT("/lecturer/exams/:id/deactivate", controllers.RoleMiddleware(models.RoleAdmin, models.RoleLecturer), lecturerCtrl.DeactivateExamination)
			staff.PUT("/lecturer/exams/:id/practice-freeze", controllers.RoleMiddleware(models.RoleAdmin, models.RoleLecturer), lecturerCtrl.SetPracticeFreeze)
			staff.PUT("/lecturer/exams/:id/introduction", controllers.RoleMiddleware(models.RoleAdmin, models.RoleLecturer), lecturerCtrl.SetExamIntroduction)
			// Exam intro images: staff mutations (upload, delete, reorder).
			staff.POST("/lecturer/exams/:id/intro-images", controllers.RoleMiddleware(models.RoleAdmin, models.RoleLecturer), lecturerCtrl.UploadExamIntroImage)
			staff.DELETE("/lecturer/exams/:id/intro-images/:imageId", controllers.RoleMiddleware(models.RoleAdmin, models.RoleLecturer), lecturerCtrl.DeleteExamIntroImage)
			staff.PUT("/lecturer/exams/:id/intro-images/order", controllers.RoleMiddleware(models.RoleAdmin, models.RoleLecturer), lecturerCtrl.ReorderExamIntroImages)
			// Per-exam curriculum schema import. Mirrors the practice category import endpoint.
			staff.POST("/lecturer/exams/:id/curriculum/import", controllers.RoleMiddleware(models.RoleAdmin, models.RoleLecturer), lecturerCtrl.ImportExamCurriculum)
			// Decoy-schema preview for the curriculum import above (dual-schema grading).
			staff.POST("/lecturer/exams/:id/curriculum/import-preview", controllers.RoleMiddleware(models.RoleAdmin, models.RoleLecturer), lecturerCtrl.ImportExamCurriculumPreview)
			// Exam category CRUD + per-category database import. Mirrors the practice
			// category routes below, scoped under the owning exam (ARCH-029).
			staff.GET("/lecturer/exams/:id/categories", controllers.RoleMiddleware(models.RoleAdmin, models.RoleLecturer), examCategoryCtrl.ListCategories)
			staff.POST("/lecturer/exams/:id/categories", controllers.RoleMiddleware(models.RoleAdmin, models.RoleLecturer), examCategoryCtrl.CreateCategory)
			staff.PUT("/lecturer/exams/:id/categories/:categoryId", controllers.RoleMiddleware(models.RoleAdmin, models.RoleLecturer), examCategoryCtrl.RenameCategory)
			staff.DELETE("/lecturer/exams/:id/categories/:categoryId", controllers.RoleMiddleware(models.RoleAdmin, models.RoleLecturer), examCategoryCtrl.DeleteCategory)
			staff.POST("/lecturer/exams/:id/categories/:categoryId/import", controllers.RoleMiddleware(models.RoleAdmin, models.RoleLecturer), examCategoryCtrl.ImportCategorySchema)
			// Decoy-schema preview for the category import above (dual-schema grading).
			staff.POST("/lecturer/exams/:id/categories/:categoryId/import-preview", controllers.RoleMiddleware(models.RoleAdmin, models.RoleLecturer), examCategoryCtrl.ImportCategoryPreviewSchema)
			// Lecturer writes the category description.
			staff.PUT("/lecturer/exams/:id/categories/:categoryId/introduction", controllers.RoleMiddleware(models.RoleAdmin, models.RoleLecturer), examCategoryCtrl.SetCategoryIntroduction)
			staff.GET("/lecturer/exams/:id/students", controllers.RoleMiddleware(models.RoleAdmin, models.RoleLecturer), lecturerCtrl.GetExamRoster)
			staff.DELETE("/lecturer/exams/:id/students/:studentId", controllers.RoleMiddleware(models.RoleAdmin, models.RoleLecturer), lecturerCtrl.RemoveStudentFromExam)
			staff.POST("/lecturer/exams/:id/students", controllers.RoleMiddleware(models.RoleAdmin, models.RoleLecturer), lecturerCtrl.AssignStudents)
			staff.GET("/lecturer/scores", controllers.RoleMiddleware(models.RoleAdmin, models.RoleLecturer), lecturerCtrl.GetConsolidatedScores)
			staff.POST("/users", controllers.RoleMiddleware(models.RoleAdmin), userCtrl.CreateAccount)
			// Single-student create/upsert — shares the importer's parity path.
			staff.POST("/students", controllers.RoleMiddleware(models.RoleAdmin, models.RoleLecturer), userCtrl.CreateStudent)

			// Submission-log export admin controls. exportCtrl is nil when SMTP is
			// not configured; the routes stay registered but report 503 in that case.
			staff.GET("/export/settings", controllers.RoleMiddleware(models.RoleAdmin), func(c *gin.Context) {
				if exportCtrl == nil {
					c.JSON(http.StatusServiceUnavailable, gin.H{"error": "email/export not configured"})
					return
				}
				exportCtrl.GetSettings(c)
			})
			staff.PUT("/export/settings", controllers.RoleMiddleware(models.RoleAdmin), func(c *gin.Context) {
				if exportCtrl == nil {
					c.JSON(http.StatusServiceUnavailable, gin.H{"error": "email/export not configured"})
					return
				}
				exportCtrl.UpdateSettings(c)
			})
			staff.POST("/export/run-now", controllers.RoleMiddleware(models.RoleAdmin), func(c *gin.Context) {
				if exportCtrl == nil {
					c.JSON(http.StatusServiceUnavailable, gin.H{"error": "email/export not configured"})
					return
				}
				exportCtrl.RunNow(c)
			})
		}

		// Practice student routes. Live during exams unless an active exam has the
		// block_practice_during_exam toggle on (toggle-aware practiceFreezeMW).
		practiceStudent := v1.Group("/practice")
		practiceStudent.Use(controllers.AuthMiddleware(authService), controllers.RoleMiddleware(models.RoleStudent), practiceFreezeMW)
		{
			practiceStudent.GET("/questions", practiceCtrl.ListForStudent)
			practiceStudent.GET("/questions/:id", practiceCtrl.GetForStudent)
			practiceStudent.POST("/submit", practiceCtrl.Submit)
			practiceStudent.POST("/preview", practiceCtrl.Preview)
			practiceStudent.GET("/attempts", practiceCtrl.ListMyAttempts)
			practiceStudent.GET("/summary", practiceCtrl.GetSummary)
			// Practice intro: schema + description for a category (available, not required).
			practiceStudent.GET("/categories/:id/introduction", practiceCtrl.GetCategoryIntroduction)
		}

		// Practice category intro images: read-only, multi-role (Student + Admin + Lecturer),
		// and explicitly NOT behind practiceFreezeMW — staff need these from the lecturer
		// editor even when practice is frozen for students, and descriptive content does
		// not need the same gating as practice attempts. Distinct group keeps the freeze
		// MW scoped to the routes that truly are "practice work".
		practiceIntroImages := v1.Group("/practice")
		practiceIntroImages.Use(controllers.AuthMiddleware(authService), controllers.RoleMiddleware(models.RoleStudent, models.RoleAdmin, models.RoleLecturer))
		{
			practiceIntroImages.GET("/categories/:id/intro-images", practiceCtrl.ListCategoryIntroImages)
			practiceIntroImages.GET("/categories/:id/intro-images/:imageId", practiceCtrl.GetCategoryIntroImage)
		}

		// Practice lecturer/admin routes (no freeze middleware)
		practiceLecturer := v1.Group("/lecturer/practice")
		practiceLecturer.Use(controllers.AuthMiddleware(authService), controllers.RoleMiddleware(models.RoleAdmin, models.RoleLecturer))
		{
			practiceLecturer.POST("/question", practiceCtrl.Upsert)
			practiceLecturer.GET("/questions", practiceCtrl.ListForLecturer)
			practiceLecturer.DELETE("/question/:id", practiceCtrl.Delete)
			practiceLecturer.POST("/preview", practiceCtrl.LecturerPreview)
			// Practice category CRUD + per-category database import.
			practiceLecturer.GET("/categories", practiceCtrl.ListCategories)
			practiceLecturer.POST("/categories", practiceCtrl.CreateCategory)
			practiceLecturer.PUT("/categories/:id", practiceCtrl.RenameCategory)
			practiceLecturer.DELETE("/categories/:id", practiceCtrl.DeleteCategory)
			practiceLecturer.POST("/categories/:id/import", practiceCtrl.ImportCategorySchema)
			// Decoy-schema preview for the import above (dual-schema grading).
			practiceLecturer.POST("/categories/:id/import-preview", practiceCtrl.ImportCategoryPreviewSchema)
			// Lecturer writes the category description.
			practiceLecturer.PUT("/categories/:id/introduction", practiceCtrl.SetCategoryIntroduction)
			// Practice category intro images: staff mutations (upload, delete, reorder).
			practiceLecturer.POST("/categories/:id/intro-images", practiceCtrl.UploadCategoryIntroImage)
			practiceLecturer.DELETE("/categories/:id/intro-images/:imageId", practiceCtrl.DeleteCategoryIntroImage)
			practiceLecturer.PUT("/categories/:id/intro-images/order", practiceCtrl.ReorderCategoryIntroImages)
			// Per-student per-question practice breakdown for the lecturer (ARCH-006).
			practiceLecturer.GET("/results", lecturerCtrl.GetPracticeResults)
		}
	}

	// Start Server
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	log.Printf("Server starting on port %s...", port)
	if err := r.Run(":" + port); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}
