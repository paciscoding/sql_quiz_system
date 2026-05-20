package services
import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/paciscoding/sql-quiz-system/internal/models"
	"github.com/paciscoding/sql-quiz-system/internal/repository"
	"golang.org/x/crypto/bcrypt"
)

type AuthService struct {
	repo          *repository.PostgresRepository
	jwtSecret     []byte
	loginAttempts sync.Map
}

func NewAuthService(repo *repository.PostgresRepository, secret string) *AuthService {
	return &AuthService{
		repo:      repo,
		jwtSecret: []byte(secret),
	}
}

func (s *AuthService) Login(ctx context.Context, username, password string) (models.LoginResponse, error) {
	// Rate Limiting: 5 attempts per 10 minutes
	now := time.Now()
	val, _ := s.loginAttempts.LoadOrStore(username, []time.Time{})
	attempts := val.([]time.Time)

	// Filter recent attempts
	var recent []time.Time
	for _, t := range attempts {
		if now.Sub(t) < 10*time.Minute {
			recent = append(recent, t)
		}
	}

	if len(recent) >= 5 {
		return models.LoginResponse{}, errors.New("too many login attempts. please try again in 10 minutes")
	}

	user, err := s.repo.GetUserByUsername(ctx, username)
	if err != nil {
		s.loginAttempts.Store(username, append(recent, now))
		return models.LoginResponse{}, errors.New("invalid username or password")
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(password)); err != nil {
		s.loginAttempts.Store(username, append(recent, now))
		return models.LoginResponse{}, errors.New("invalid username or password")
	}

	// Success: clear attempts
	s.loginAttempts.Delete(username)

	token, err := s.generateToken(user)
	if err != nil {
		return models.LoginResponse{}, err
	}


	user.Password = "" // Clear hash before returning
	return models.LoginResponse{
		Token: token,
		User:  user,
	}, nil
}

func (s *AuthService) ListUsersByRole(ctx context.Context, role models.Role) ([]models.User, error) {
	return s.repo.GetAllUsersByRole(ctx, role)
}

func (s *AuthService) GetAllUsers(ctx context.Context) ([]models.User, error) {
	return s.repo.GetAllUsers(ctx)
}

func (s *AuthService) UpdateUser(ctx context.Context, u models.User) error {
	return s.repo.UpdateUser(ctx, u)
}

func (s *AuthService) DeleteUser(ctx context.Context, id int) error {
	return s.repo.DeleteUser(ctx, id)
}

func (s *AuthService) CreateAccount(ctx context.Context, creator models.User, req models.CreateUserRequest) (int, error) {
	// Role Hierarchical Validation
	if creator.Role == models.RoleLecturer && req.Role != models.RoleStudent {
		return 0, errors.New("lecturers can only create student accounts")
	}
	if creator.Role == models.RoleStudent {
		return 0, errors.New("students cannot create accounts")
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		return 0, err
	}

	user := models.User{
		Username:  req.Username,
		Password:  string(hash),
		Role:      req.Role,
		CreatedBy: &creator.ID,
	}

	return s.repo.CreateUser(ctx, user)
}

func (s *AuthService) CreateAccountsBulk(ctx context.Context, creator models.User, req models.BulkCreateUserRequest) ([]int, error) {
	var ids []int
	for _, userReq := range req.Users {
		id, err := s.CreateAccount(ctx, creator, userReq)
		if err != nil {
			return ids, fmt.Errorf("failed at user %s: %w", userReq.Username, err)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func (s *AuthService) SeedAdminAccount(ctx context.Context, username, password string) error {
	_, err := s.repo.GetUserByUsername(ctx, username)
	if err == nil {
		return nil // Admin exists
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}

	user := models.User{
		Username: username,
		Password: string(hash),
		Role:     models.RoleAdmin,
	}

	_, err = s.repo.CreateUser(ctx, user)
	return err
}

func (s *AuthService) ChangePassword(ctx context.Context, userID int, oldPassword, newPassword string) error {
	user, err := s.repo.GetUserByID(ctx, userID)
	if err != nil {
		return err
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(oldPassword)); err != nil {
		return errors.New("invalid old password")
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}

	return s.repo.UpdateUserPassword(ctx, userID, string(hash))
}

func (s *AuthService) generateToken(user models.User) (string, error) {
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"user_id":  user.ID,
		"username": user.Username,
		"role":     user.Role,
		"exp":      time.Now().Add(time.Hour * 24).Unix(),
	})

	return token.SignedString(s.jwtSecret)
}

func (s *AuthService) ValidateToken(tokenString string) (models.User, error) {
	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return s.jwtSecret, nil
	}, jwt.WithValidMethods([]string{"HS256"}))

	if err != nil || !token.Valid {
		return models.User{}, errors.New("invalid token")
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return models.User{}, errors.New("invalid claims")
	}

	userID, ok1 := claims["user_id"].(float64)
	username, ok2 := claims["username"].(string)
	role, ok3 := claims["role"].(string)

	if !ok1 || !ok2 || !ok3 {
		return models.User{}, errors.New("invalid claims format")
	}

	return models.User{
		ID:       int(userID),
		Username: username,
		Role:     models.Role(role),
	}, nil
}
