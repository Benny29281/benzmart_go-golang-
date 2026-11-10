package auth

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

type stubRepo struct {
	Repository
	user    User
	session Session
	revoked bool
}

func (r *stubRepo) CreateUser(_ context.Context, u User) error { r.user = u; return nil }
func (r *stubRepo) UserByEmail(_ context.Context, email string) (User, error) {
	if email != r.user.Email {
		return User{}, sql.ErrNoRows
	}
	return r.user, nil
}
func (r *stubRepo) UserByID(_ context.Context, id uuid.UUID) (User, error) {
	if id != r.user.ID {
		return User{}, sql.ErrNoRows
	}
	return r.user, nil
}
func (r *stubRepo) SaveSession(_ context.Context, session Session) error {
	r.session = session
	return nil
}
func (r *stubRepo) RevokeSession(_ context.Context, _ uuid.UUID, _ []byte, _ time.Time) error {
	r.revoked = true
	return nil
}

func testService(t *testing.T) (*Service, *stubRepo) {
	t.Helper()
	repo := new(stubRepo)
	s, err := NewService(repo, strings.Repeat("s", 32))
	if err != nil {
		t.Fatal(err)
	}
	return s, repo
}

func TestRegisterLoginAndAuthentication(t *testing.T) {
	s, repo := testService(t)
	ctx := context.Background()
	u, err := s.Register(ctx, " Benny ", " BENNY@example.com ", "password123", "customer")
	if err != nil {
		t.Fatal(err)
	}
	if u.Name != "Benny" || u.Email != "benny@example.com" || u.Role != "customer" || u.ID.Version() != 7 {
		t.Fatalf("unexpected user: %+v", u)
	}
	if cost, err := bcrypt.Cost([]byte(repo.user.PasswordHash)); err != nil || cost < 12 {
		t.Fatalf("invalid password cost: %d %v", cost, err)
	}
	tokens, err := s.Login(ctx, "benny@example.com", "password123")
	if err != nil {
		t.Fatal(err)
	}
	if len(repo.session.Hash) != 32 || string(repo.session.Hash) == tokens.RefreshToken || repo.session.UserID != u.ID {
		t.Fatal("refresh token must be stored hashed and owned by user")
	}
	if _, err := s.Authenticate(ctx, tokens.AccessToken); err != nil {
		t.Fatal(err)
	}
	if err := s.Logout(ctx, u.ID, tokens.RefreshToken); err != nil || !repo.revoked {
		t.Fatalf("logout failed: %v", err)
	}
	for _, email := range []string{"benny@example.com", "missing@example.com"} {
		if _, err := s.Login(ctx, email, "wrong-password"); !errors.Is(err, ErrCredentials) {
			t.Fatalf("expected generic credentials error: %v", err)
		}
	}
}

func TestRegisterValidation(t *testing.T) {
	s, _ := testService(t)
	for _, tc := range []struct{ name, email, password, role string }{
		{"", "test@example.com", "password123", "customer"},
		{"Benny", "not-email", "password123", "customer"},
		{"Benny", "Name <test@example.com>", "password123", "customer"},
		{"Benny", "test@example.com", "short", "customer"},
		{"Benny", "test@example.com", strings.Repeat("a", 73), "customer"},
		{"Benny", "test@example.com", "password123", "superadmin"},
	} {
		if _, err := s.Register(context.Background(), tc.name, tc.email, tc.password, tc.role); !errors.Is(err, ErrInvalid) {
			t.Fatalf("expected invalid input, got %v", err)
		}
	}
}

func TestAuthenticationRejectsInvalidJWTs(t *testing.T) {
	s, repo := testService(t)
	repo.user = User{ID: uuid.New(), Role: "customer"}
	for _, tc := range []struct {
		name    string
		method  jwt.SigningMethod
		key     any
		expires time.Time
		issuer  string
	}{
		{"expired", jwt.SigningMethodHS256, s.secret, time.Now().Add(-time.Minute), issuer},
		{"wrong key", jwt.SigningMethodHS256, []byte(strings.Repeat("x", 32)), time.Now().Add(time.Hour), issuer},
		{"wrong algorithm", jwt.SigningMethodHS384, s.secret, time.Now().Add(time.Hour), issuer},
		{"wrong issuer", jwt.SigningMethodHS256, s.secret, time.Now().Add(time.Hour), "someone-else"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := jwt.NewWithClaims(tc.method, jwt.RegisteredClaims{Subject: repo.user.ID.String(), Issuer: tc.issuer, Audience: jwt.ClaimStrings{"benzmart-api"}, ExpiresAt: jwt.NewNumericDate(tc.expires)}).SignedString(tc.key)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Authenticate(context.Background(), raw); !errors.Is(err, ErrToken) {
				t.Fatalf("accepted invalid JWT: %v", err)
			}
		})
	}
	// Expiration is mandatory, even for a correctly signed token.
	raw, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{Subject: repo.user.ID.String(), Issuer: issuer, Audience: jwt.ClaimStrings{"benzmart-api"}}).SignedString(s.secret)
	if _, err := s.Authenticate(context.Background(), raw); !errors.Is(err, ErrToken) {
		t.Fatalf("accepted missing expiration: %v", err)
	}
}
