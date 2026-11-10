package auth

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
)

type MySQLRepository struct{ DB *sql.DB }

func (r *MySQLRepository) CreateUser(ctx context.Context, u User) error {
	_, err := r.DB.ExecContext(ctx, `INSERT INTO users
        (id,email,password_hash,name,role,created_at,updated_at) VALUES (?,?,?,?,?,?,?)`,
		u.ID[:], u.Email, u.PasswordHash, u.Name, u.Role, u.CreatedAt, u.CreatedAt)
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 {
		return ErrDuplicate
	}
	return err
}

type scanner interface{ Scan(...any) error }

func scanUser(row scanner) (User, error) {
	var u User
	var id []byte
	if err := row.Scan(&id, &u.Email, &u.PasswordHash, &u.Name, &u.Role, &u.CreatedAt); err != nil {
		return User{}, err
	}
	var err error
	u.ID, err = uuid.FromBytes(id)
	return u, err
}

const userColumns = "id,email,password_hash,name,role,created_at"

func (r *MySQLRepository) UserByEmail(ctx context.Context, email string) (User, error) {
	return scanUser(r.DB.QueryRowContext(ctx, "SELECT "+userColumns+" FROM users WHERE email=?", email))
}

func (r *MySQLRepository) UserByID(ctx context.Context, id uuid.UUID) (User, error) {
	return scanUser(r.DB.QueryRowContext(ctx, "SELECT "+userColumns+" FROM users WHERE id=?", id[:]))
}

func insertSession(ctx context.Context, db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, s Session) error {
	_, err := db.ExecContext(ctx, `INSERT INTO refresh_tokens (id,user_id,token_hash,expires_at) VALUES (?,?,?,?)`,
		s.ID[:], s.UserID[:], s.Hash, s.ExpiresAt)
	return err
}

func (r *MySQLRepository) SaveSession(ctx context.Context, s Session) error {
	return insertSession(ctx, r.DB, s)
}

// Row locking ensures a refresh token can only be exchanged once, even concurrently.
func (r *MySQLRepository) RotateSession(ctx context.Context, oldHash []byte, next Session, now time.Time) (User, error) {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()
	var userID []byte
	var expires time.Time
	var revoked sql.NullTime
	err = tx.QueryRowContext(ctx, `SELECT user_id,expires_at,revoked_at FROM refresh_tokens WHERE token_hash=? FOR UPDATE`, oldHash).
		Scan(&userID, &expires, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrToken
	}
	if err != nil {
		return User{}, err
	}
	if revoked.Valid || !expires.After(now) {
		return User{}, ErrToken
	}
	u, err := scanUser(tx.QueryRowContext(ctx, "SELECT "+userColumns+" FROM users WHERE id=?", userID))
	if err != nil {
		return User{}, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE refresh_tokens SET revoked_at=? WHERE token_hash=?", now, oldHash); err != nil {
		return User{}, err
	}
	next.UserID = u.ID
	if err = insertSession(ctx, tx, next); err != nil {
		return User{}, err
	}
	if err = tx.Commit(); err != nil {
		return User{}, err
	}
	return u, nil
}

func (r *MySQLRepository) RevokeSession(ctx context.Context, userID uuid.UUID, hash []byte, now time.Time) error {
	_, err := r.DB.ExecContext(ctx, `UPDATE refresh_tokens SET revoked_at=COALESCE(revoked_at,?) WHERE user_id=? AND token_hash=?`, now, userID[:], hash)
	return err
}
