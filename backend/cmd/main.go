package main

import (
	"log"
	"os"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/paciscoding/sql-quiz-system/internal/controllers"
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

	ctx := context.Background()

	// Setup restricted role for student queries
	if err := repo.CreateRestrictedUser(ctx); err != nil {
		log.Fatalf("Failed to setup restricted database role: %v", err)
	}

	// Initialize System Tables
	if err := repo.InitializeSystemTables(ctx); err != nil {
		log.Fatalf("Failed to initialize system tables: %v", err)
	}

	// Initialize Services
	valService := services.NewValidationService(repo)
	lockManager := services.NewLockManager(repo.Pool())
	
	jwtSecret := os.Getenv("JWT_SECRET")
	if len(jwtSecret) < 32 {
		log.Fatalf("JWT_SECRET must be at least 32 characters long")
	}
	authService := services.NewAuthService(repo, jwtSecret)

	// Seed Admin
	if err := authService.SeedAdminAccount(ctx, "admin", "admin123"); err != nil {
		log.Printf("Warning: Failed to seed admin: %v", err)
	}

	// Initialize Controllers
	quizCtrl := controllers.NewQuizController(valService, lockManager, repo)
	lecturerCtrl := controllers.NewLecturerController(repo)
	userCtrl := controllers.NewUserController(authService)

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
		}

		// Protected Quiz Routes
		quiz := v1.Group("/quiz")
		quiz.Use(controllers.AuthMiddleware(authService))
		{
			quiz.POST("/submit", controllers.RoleMiddleware(models.RoleStudent), quizCtrl.SubmitAnswer)
			quiz.GET("/exams", controllers.RoleMiddleware(models.RoleStudent), quizCtrl.GetExams)
			quiz.GET("/exams/:id/questions", controllers.RoleMiddleware(models.RoleStudent), quizCtrl.GetExamQuestions)
		}

		// Protected Lecturer/Admin Routes
		staff := v1.Group("/")
		staff.Use(controllers.AuthMiddleware(authService))
		{
			staff.POST("/lecturer/question", controllers.RoleMiddleware(models.RoleAdmin, models.RoleLecturer), lecturerCtrl.UpsertQuestion)
			staff.POST("/lecturer/import-schema", controllers.RoleMiddleware(models.RoleAdmin, models.RoleLecturer), lecturerCtrl.ImportSchema)
			staff.GET("/lecturer/exams", controllers.RoleMiddleware(models.RoleAdmin, models.RoleLecturer), lecturerCtrl.ListExaminations)
			staff.POST("/lecturer/exams", controllers.RoleMiddleware(models.RoleAdmin, models.RoleLecturer), lecturerCtrl.CreateExamination)
			staff.PUT("/lecturer/exams/:id/activate", controllers.RoleMiddleware(models.RoleAdmin, models.RoleLecturer), lecturerCtrl.ActivateExamination)
			staff.POST("/lecturer/exams/:id/students", controllers.RoleMiddleware(models.RoleAdmin, models.RoleLecturer), lecturerCtrl.AssignStudents)
			staff.POST("/users", controllers.RoleMiddleware(models.RoleAdmin, models.RoleLecturer), userCtrl.CreateAccount)
			staff.POST("/users/bulk", controllers.RoleMiddleware(models.RoleAdmin, models.RoleLecturer), userCtrl.CreateAccountsBulk)
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
