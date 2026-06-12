package main

import (
	"context"
	"log"
	"os"
	"strings"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/paciscoding/sql-quiz-system/internal/controllers"
	"github.com/paciscoding/sql-quiz-system/internal/middleware"
	"github.com/paciscoding/sql-quiz-system/internal/models"
	"github.com/paciscoding/sql-quiz-system/internal/repository"
	"github.com/paciscoding/sql-quiz-system/internal/services"
)

func main() {
	// Database connection string from environment or default
	connString := os.Getenv("DATABASE_URL")
	if connString == "" {
		// Default for local development
		connString = "postgres://postgres:password@localhost:5432/sql_quiz?sslmode=disable"
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

	// Seed Admin
	adminUser := os.Getenv("ADMIN_USER")
	if adminUser == "" {
		adminUser = "admin"
	}
	adminPass := os.Getenv("ADMIN_PASS")
	if adminPass == "" {
		adminPass = "admin123"
	}

	if err := authService.SeedAdminAccount(ctx, adminUser, adminPass); err != nil {
		log.Printf("Warning: Failed to seed admin: %v", err)
	}

	// Initialize Controllers
	quizCtrl := controllers.NewQuizController(valService, submissionQueue, repo, hub, authService)
	lecturerCtrl := controllers.NewLecturerController(repo, hub)
	lecturerCtrl.SetStudentImportService(studentImportService)
	userCtrl := controllers.NewUserController(authService)
	practiceCtrl := controllers.NewPracticeController(repo, valService, authService)
	// Practice is live during exams by default; this gate only blocks practice
	// when an active exam has its block_practice_during_exam toggle on (OQ-1).
	practiceFreezeMW := middleware.PracticeFreezeMiddleware(repo)

	// Setup Gin
	r := gin.Default()

	config := cors.DefaultConfig()
	allowedOrigins := os.Getenv("ALLOWED_ORIGINS")
	if allowedOrigins != "" {
		config.AllowOrigins = strings.Split(allowedOrigins, ",")
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
			staff.PUT("/lecturer/exams/:id/activate", controllers.RoleMiddleware(models.RoleAdmin, models.RoleLecturer), lecturerCtrl.ActivateExamination)
			staff.PUT("/lecturer/exams/:id/deactivate", controllers.RoleMiddleware(models.RoleAdmin, models.RoleLecturer), lecturerCtrl.DeactivateExamination)
			staff.PUT("/lecturer/exams/:id/practice-freeze", controllers.RoleMiddleware(models.RoleAdmin, models.RoleLecturer), lecturerCtrl.SetPracticeFreeze)
			staff.PUT("/lecturer/exams/:id/introduction", controllers.RoleMiddleware(models.RoleAdmin, models.RoleLecturer), lecturerCtrl.SetExamIntroduction)
			// Exam intro images: staff mutations (upload, delete, reorder).
			staff.POST("/lecturer/exams/:id/intro-images", controllers.RoleMiddleware(models.RoleAdmin, models.RoleLecturer), lecturerCtrl.UploadExamIntroImage)
			staff.DELETE("/lecturer/exams/:id/intro-images/:imageId", controllers.RoleMiddleware(models.RoleAdmin, models.RoleLecturer), lecturerCtrl.DeleteExamIntroImage)
			staff.PUT("/lecturer/exams/:id/intro-images/order", controllers.RoleMiddleware(models.RoleAdmin, models.RoleLecturer), lecturerCtrl.ReorderExamIntroImages)
			staff.GET("/lecturer/exams/:id/students", controllers.RoleMiddleware(models.RoleAdmin, models.RoleLecturer), lecturerCtrl.GetExamRoster)
			staff.DELETE("/lecturer/exams/:id/students/:studentId", controllers.RoleMiddleware(models.RoleAdmin, models.RoleLecturer), lecturerCtrl.RemoveStudentFromExam)
			staff.POST("/lecturer/exams/:id/students", controllers.RoleMiddleware(models.RoleAdmin, models.RoleLecturer), lecturerCtrl.AssignStudents)
			staff.GET("/lecturer/scores", controllers.RoleMiddleware(models.RoleAdmin, models.RoleLecturer), lecturerCtrl.GetConsolidatedScores)
			staff.GET("/lecturer/export-scores", controllers.RoleMiddleware(models.RoleAdmin, models.RoleLecturer), lecturerCtrl.ExportScoresCSV)
			staff.POST("/users", controllers.RoleMiddleware(models.RoleAdmin, models.RoleLecturer), userCtrl.CreateAccount)
			staff.POST("/users/bulk", controllers.RoleMiddleware(models.RoleAdmin, models.RoleLecturer), userCtrl.CreateAccountsBulk)
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
			// Lecturer writes the category description.
			practiceLecturer.PUT("/categories/:id/introduction", practiceCtrl.SetCategoryIntroduction)
			// Practice category intro images: staff mutations (upload, delete, reorder).
			practiceLecturer.POST("/categories/:id/intro-images", practiceCtrl.UploadCategoryIntroImage)
			practiceLecturer.DELETE("/categories/:id/intro-images/:imageId", practiceCtrl.DeleteCategoryIntroImage)
			practiceLecturer.PUT("/categories/:id/intro-images/order", practiceCtrl.ReorderCategoryIntroImages)
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
