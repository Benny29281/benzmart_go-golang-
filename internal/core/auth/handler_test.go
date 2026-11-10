package auth

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

type stubLimiter struct {
	allowed bool
	err     error
}

func (l stubLimiter) Allow(context.Context, string, int) (bool, error) { return l.allowed, l.err }

func TestHTTPAuthorizationAndValidation(t *testing.T) {
	s, repo := testService(t)
	repo.user = User{ID: uuid.New(), Role: "customer"}
	tokens, err := s.tokens(repo.user, "")
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{Service: s, Limiter: stubLimiter{allowed: true}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	router := Correlation(h.Routes())
	for _, tc := range []struct {
		name, method, path, body, token string
		status                          int
	}{
		{"missing auth", "GET", "/api/v1/auth/me", "", "", 401},
		{"customer profile", "GET", "/api/v1/auth/me", "", tokens.AccessToken, 200},
		{"customer denied admin", "GET", "/api/v1/admin/me", "", tokens.AccessToken, 403},
		{"reject role escalation", "POST", "/api/v1/auth/register", `{"name":"Benny","email":"test@example.com","password":"password123","role":"admin"}`, "", 400},
		{"reject trailing json", "POST", "/api/v1/auth/login", `{"email":"test@example.com","password":"password123"}{}`, "", 400},
		{"reject malformed json", "POST", "/api/v1/auth/login", `{`, "", 400},
		{"reject oversized body", "POST", "/api/v1/auth/login", `{"email":"` + strings.Repeat("x", 17000) + `"}`, "", 400},
		{"reject missing refresh", "POST", "/api/v1/auth/refresh", `{}`, "", 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("status=%d, want=%d body=%s", rec.Code, tc.status, rec.Body.String())
			}
			if rec.Header().Get("X-Correlation-ID") == "" {
				t.Fatal("missing correlation ID")
			}
			if strings.Contains(rec.Body.String(), "password_hash") {
				t.Fatal("password hash leaked")
			}
		})
	}
	repo.user.Role = "admin"
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/admin/me", nil)
	req.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
	router.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("admin denied: %d", rec.Code)
	}
}

func TestRateLimitAndRedisFailure(t *testing.T) {
	h := &Handler{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	for _, tc := range []struct {
		limiter stubLimiter
		status  int
	}{
		{stubLimiter{allowed: false}, 429},
		{stubLimiter{err: errors.New("redis unavailable")}, 503},
	} {
		h.Limiter = tc.limiter
		rec := httptest.NewRecorder()
		Correlation(h.Routes()).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{}`)))
		if rec.Code != tc.status {
			t.Fatalf("status=%d want=%d", rec.Code, tc.status)
		}
	}
}
