package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

const issuer = "benzmart"

type Service struct {
	repo      Repository
	secret    []byte
	dummyHash []byte
}

func NewService(repo Repository, secret string) (*Service, error) {
	if len(secret) < 32 {
		return nil, errors.New("JWT_SECRET minimal 32 byte")
	}
	dummy, err := bcrypt.GenerateFromPassword([]byte("not-a-real-account-password"), 12)
	if err != nil {
		return nil, err
	}
	return &Service{repo: repo, secret: []byte(secret), dummyHash: dummy}, nil
}

func normalizeEmail(email string) (string, bool) {
	email = strings.ToLower(strings.TrimSpace(email))
	address, err := mail.ParseAddress(email)
	return email, err == nil && address.Address == email && len(email) <= 254
}

func (s *Service) Register(ctx context.Context, name, email, password, role string) (User, error) {
	email, valid := normalizeEmail(email)
	name = strings.TrimSpace(name)
	if !valid || name == "" || utf8.RuneCountInString(name) > 100 || len(password) < 8 || len(password) > 72 || (role != "customer" && role != "admin") {
		return User{}, ErrInvalid
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return User{}, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return User{}, err
	}
	u := User{ID: id, Email: email, Name: name, Role: role, PasswordHash: string(hash), CreatedAt: time.Now().UTC()}
	if err = s.repo.CreateUser(ctx, u); err != nil {
		return User{}, err
	}
	return u, nil
}

type Tokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
}

func (s *Service) Login(ctx context.Context, email, password string) (Tokens, error) {
	email, valid := normalizeEmail(email)
	if !valid || len(password) > 72 || password == "" {
		return Tokens{}, ErrCredentials
	}
	u, err := s.repo.UserByEmail(ctx, email)
	if errors.Is(err, sql.ErrNoRows) {
		_ = bcrypt.CompareHashAndPassword(s.dummyHash, []byte(password))
		return Tokens{}, ErrCredentials
	}
	if err != nil {
		return Tokens{}, err
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)) != nil {
		return Tokens{}, ErrCredentials
	}
	raw, session, err := newSession()
	if err != nil {
		return Tokens{}, err
	}
	session.UserID = u.ID
	tokens, err := s.tokens(u, raw)
	if err != nil {
		return Tokens{}, err
	}
	if err = s.repo.SaveSession(ctx, session); err != nil {
		return Tokens{}, err
	}
	return tokens, nil
}

func newSession() (string, Session, error) {
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return "", Session{}, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return "", Session{}, err
	}
	raw := base64.RawURLEncoding.EncodeToString(random)
	hash := sha256.Sum256([]byte(raw))
	return raw, Session{ID: id, Hash: hash[:], ExpiresAt: time.Now().UTC().Add(7 * 24 * time.Hour)}, nil
}

func tokenHash(raw string) ([]byte, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(decoded) != 32 {
		return nil, ErrToken
	}
	hash := sha256.Sum256([]byte(raw))
	return hash[:], nil
}

func (s *Service) Refresh(ctx context.Context, old string) (Tokens, error) {
	hash, err := tokenHash(old)
	if err != nil {
		return Tokens{}, err
	}
	raw, session, err := newSession()
	if err != nil {
		return Tokens{}, err
	}
	u, err := s.repo.RotateSession(ctx, hash, session, time.Now().UTC())
	if err != nil {
		return Tokens{}, err
	}
	return s.tokens(u, raw)
}

func (s *Service) Logout(ctx context.Context, userID uuid.UUID, raw string) error {
	hash, err := tokenHash(raw)
	if err != nil {
		return err
	}
	return s.repo.RevokeSession(ctx, userID, hash, time.Now().UTC())
}

func (s *Service) tokens(u User, refresh string) (Tokens, error) {
	now := time.Now().UTC()
	claims := jwt.RegisteredClaims{
		Issuer: issuer, Subject: u.ID.String(), Audience: jwt.ClaimStrings{"benzmart-api"},
		IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(15 * time.Minute)),
	}
	access, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(s.secret)
	return Tokens{AccessToken: access, RefreshToken: refresh, TokenType: "Bearer", ExpiresIn: 900}, err
}

func (s *Service) Authenticate(ctx context.Context, raw string) (User, error) {
	claims := new(jwt.RegisteredClaims)
	token, err := jwt.ParseWithClaims(raw, claims, func(*jwt.Token) (any, error) { return s.secret, nil },
		jwt.WithValidMethods([]string{"HS256"}), jwt.WithIssuer(issuer), jwt.WithAudience("benzmart-api"), jwt.WithExpirationRequired())
	if err != nil || !token.Valid {
		return User{}, ErrToken
	}
	id, err := uuid.Parse(claims.Subject)
	if err != nil {
		return User{}, ErrToken
	}
	u, err := s.repo.UserByID(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrToken
	}
	return u, err
}
