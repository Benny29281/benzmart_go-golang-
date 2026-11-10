package auth

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

var (
	ErrInvalid     = errors.New("data tidak valid")
	ErrCredentials = errors.New("email atau password salah")
	ErrDuplicate   = errors.New("email sudah terdaftar")
	ErrToken       = errors.New("token tidak valid atau kedaluwarsa")
)

type User struct {
	ID           uuid.UUID `json:"id"`
	Email        string    `json:"email"`
	Name         string    `json:"name"`
	Role         string    `json:"role"`
	PasswordHash string    `json:"-"`
	CreatedAt    time.Time `json:"created_at"`
}

type Session struct {
	ID        uuid.UUID
	UserID    uuid.UUID
	Hash      []byte
	ExpiresAt time.Time
}

type Repository interface {
	CreateUser(context.Context, User) error
	UserByEmail(context.Context, string) (User, error)
	UserByID(context.Context, uuid.UUID) (User, error)
	SaveSession(context.Context, Session) error
	RotateSession(context.Context, []byte, Session, time.Time) (User, error)
	RevokeSession(context.Context, uuid.UUID, []byte, time.Time) error
}
