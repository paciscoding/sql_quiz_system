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
	quizCtrl := controllers.NewQuizController(valService, submissionQueue, repo)
	lecturerCtrl := controllers.NewLecturerController(repo)
	lecturerCtrl.SetStudentImportService(studentImportService)
	userCtrl := controllers.NewUserController(authService)
	practiceCtrl := controllers.NewPracticeController(repo, valService, authService)
	examFreezeMW := middleware.ExamFreezeMiddleware(repo)

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

		// Protected Quiz Routes
		quiz := v1.Group("/quiz")
		quiz.Use(controllers.AuthMiddleware(authService))
		{
			quiz.POST("/submit", controllers.RoleMiddleware(models.RoleStudent), quizCtrl.SubmitAnswer)
			quiz.POST("/preview", controllers.RoleMiddleware(models.RoleStudent), quizCtrl.PreviewAnswer)
			quiz.GET("/exams", controllers.RoleMiddleware(models.RoleStudent), quizCtrl.GetExams)
			quiz.GET("/exams/:id/questions", controllers.RoleMiddleware(models.RoleStudent), quizCtrl.GetExamQuestions)
			quiz.GET("/exams/:id/results", controllers.RoleMiddleware(models.RoleStudent), quizCtrl.GetExamResults)
			quiz.GET("/results", controllers.RoleMiddleware(models.RoleStudent), quizCtrl.GetMyResultsSummary)
		}

		// Protected Lecturer/Admin Routes
		staff := v1.Group("/")
		staff.Use(controllers.AuthMiddleware(authService))
		{
			staff.POST("/lecturer/question", controllers.RoleMiddleware(models.RoleAdmin, models.RoleLecturer), lecturerCtrl.UpsertQuestion)
			staff.GET("/lecturer/exams/:id/questions", controllers.RoleMiddleware(models.RoleAdmin, models.RoleLecturer), lecturerCtrl.GetExamQuestions)
			staff.DELETE("/lecturer/questions/:id", controllers.RoleMiddleware(models.RoleAdmin, models.RoleLecturer), lecturerCtrl.DeleteQuestion)
			staff.POST("/lecturer/preview", controllers.RoleMiddleware(models.RoleAdmin, models.RoleLecturer), lecturerCtrl.PreviewCorrectSQL)
			staff.POST("/lecturer/import-schema", controllers.RoleMiddleware(models.RoleAdmin), lecturerCtrl.ImportSchema)
			staff.POST("/lecturer/students/import", controllers.RoleMiddleware(models.RoleAdmin, models.RoleLecturer), lecturerCtrl.ImportStudents)
			staff.GET("/lecturer/exams", controllers.RoleMiddleware(models.RoleAdmin, models.RoleLecturer), lecturerCtrl.ListExaminations)
			staff.POST("/lecturer/exams", controllers.RoleMiddleware(models.RoleAdmin, models.RoleLecturer), lecturerCtrl.CreateExamination)
			staff.GET("/lecturer/exams/:id/submissions", controllers.RoleMiddleware(models.RoleAdmin, models.RoleLecturer), lecturerCtrl.ListSubmissions)
			staff.PUT("/lecturer/exams/:id/activate", controllers.RoleMiddleware(models.RoleAdmin, models.RoleLecturer), lecturerCtrl.ActivateExamination)
			staff.PUT("/lecturer/exams/:id/deactivate", controllers.RoleMiddleware(models.RoleAdmin, models.RoleLecturer), lecturerCtrl.DeactivateExamination)
			staff.POST("/lecturer/exams/:id/students", controllers.RoleMiddleware(models.RoleAdmin, models.RoleLecturer), lecturerCtrl.AssignStudents)
			staff.GET("/lecturer/scores", controllers.RoleMiddleware(models.RoleAdmin, models.RoleLecturer), lecturerCtrl.GetConsolidatedScores)
			staff.GET("/lecturer/export-scores", controllers.RoleMiddleware(models.RoleAdmin, models.RoleLecturer), lecturerCtrl.ExportScoresCSV)
			staff.POST("/users", controllers.RoleMiddleware(models.RoleAdmin, models.RoleLecturer), userCtrl.CreateAccount)
			staff.POST("/users/bulk", controllers.RoleMiddleware(models.RoleAdmin, models.RoleLecturer), userCtrl.CreateAccountsBulk)
		}

		// Practice student routes (with exam freeze middleware)
		practiceStudent := v1.Group("/practice")
		practiceStudent.Use(controllers.AuthMiddleware(authService), controllers.RoleMiddleware(models.RoleStudent), examFreezeMW)
		{
			practiceStudent.GET("/questions", practiceCtrl.ListForStudent)
			practiceStudent.GET("/questions/:id", practiceCtrl.GetForStudent)
			practiceStudent.POST("/submit", practiceCtrl.Submit)
			practiceStudent.POST("/preview", practiceCtrl.Preview)
			practiceStudent.GET("/attempts", practiceCtrl.ListMyAttempts)
			practiceStudent.GET("/summary", practiceCtrl.GetSummary)
		}

		// Practice lecturer/admin routes (no freeze middleware)
		practiceLecturer := v1.Group("/lecturer/practice")
		practiceLecturer.Use(controllers.AuthMiddleware(authService), controllers.RoleMiddleware(models.RoleAdmin, models.RoleLecturer))
		{
			practiceLecturer.POST("/question", practiceCtrl.Upsert)
			practiceLecturer.GET("/questions", practiceCtrl.ListForLecturer)
			practiceLecturer.DELETE("/question/:id", practiceCtrl.Delete)
			practiceLecturer.POST("/reset-sandbox", practiceCtrl.ResetSandbox)
			practiceLecturer.POST("/preview", practiceCtrl.LecturerPreview)
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
