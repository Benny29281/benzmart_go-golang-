package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"benzmart/migrations"
	"github.com/alicebob/miniredis/v2"
	"github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

func TestRedisRateLimitExpires(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	defer client.Close()
	limiter := RedisLimiter{Client: client}
	for i := 0; i < 6; i++ {
		allowed, err := limiter.Allow(context.Background(), "login", 5)
		if err != nil || allowed != (i < 5) {
			t.Fatalf("request %d allowed=%v err=%v", i, allowed, err)
		}
	}
	server.FastForward(61 * time.Second)
	if allowed, err := limiter.Allow(context.Background(), "login", 5); err != nil || !allowed {
		t.Fatalf("window did not expire: %v", err)
	}
}

// Opt-in test: the database must already exist and have a name ending in _test.
// Only rows created by this test are deleted on cleanup.
func TestMySQLAuthenticationFlow(t *testing.T) {
	dsn := os.Getenv("BENZMART_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("set BENZMART_TEST_MYSQL_DSN for MySQL integration test")
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil || !strings.HasSuffix(cfg.DBName, "_test") || !cfg.ParseTime {
		t.Fatal("test DSN must use a *_test database and parseTime=true")
	}
	if err := migrations.Up(dsn); err != nil {
		t.Fatal(err)
	}
	if err := migrations.Up(dsn); err != nil {
		t.Fatalf("migration not idempotent: %v", err)
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := &MySQLRepository{DB: db}
	service, err := NewService(repo, strings.Repeat("integration-secret-", 3))
	if err != nil {
		t.Fatal(err)
	}
	cache := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: cache.Addr()})
	defer client.Close()
	handler := &Handler{Service: service, Limiter: RedisLimiter{Client: client}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	server := httptest.NewServer(Correlation(handler.Routes()))
	defer server.Close()
	email := uuid.NewString() + "@example.com"
	defer func() {
		_, _ = db.Exec("DELETE FROM refresh_tokens WHERE user_id IN (SELECT id FROM users WHERE email=?)", email)
		_, _ = db.Exec("DELETE FROM users WHERE email=?", email)
	}()
	request := func(method, path, body, token string) (int, []byte) {
		t.Helper()
		req, err := http.NewRequest(method, server.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		data, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		return res.StatusCode, data
	}
	register := fmt.Sprintf(`{"name":"Integration Customer","email":%q,"password":"password123"}`, email)
	if status, data := request("POST", "/api/v1/auth/register", register, ""); status != 201 {
		t.Fatalf("register: %d %s", status, data)
	}
	if status, data := request("POST", "/api/v1/auth/register", register, ""); status != 409 {
		t.Fatalf("duplicate: %d %s", status, data)
	}
	status, data := request("POST", "/api/v1/auth/login", fmt.Sprintf(`{"email":%q,"password":"password123"}`, email), "")
	if status != 200 {
		t.Fatalf("login: %d %s", status, data)
	}
	var tokens Tokens
	if err := json.Unmarshal(data, &tokens); err != nil {
		t.Fatal(err)
	}
	if status, data := request("GET", "/api/v1/auth/me", "", tokens.AccessToken); status != 200 {
		t.Fatalf("profile: %d %s", status, data)
	}
	if status, _ := request("GET", "/api/v1/admin/me", "", tokens.AccessToken); status != 403 {
		t.Fatalf("customer admin access: %d", status)
	}
	u, err := repo.UserByEmail(context.Background(), email)
	if err != nil {
		t.Fatal(err)
	}
	var successes int
	var next Tokens
	var mu sync.Mutex
	var wg sync.WaitGroup
	// All callers race to exchange the same token; one transaction may win.
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := service.Refresh(context.Background(), tokens.RefreshToken)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				successes++
				next = got
			} else if err != ErrToken {
				t.Errorf("refresh error: %v", err)
			}
		}()
	}
	wg.Wait()
	if successes != 1 {
		t.Fatalf("refresh successes=%d, want 1", successes)
	}
	if _, err := service.Refresh(context.Background(), tokens.RefreshToken); err != ErrToken {
		t.Fatalf("replay accepted: %v", err)
	}
	if status, data := request("POST", "/api/v1/auth/logout", fmt.Sprintf(`{"refresh_token":%q}`, next.RefreshToken), next.AccessToken); status != 204 {
		t.Fatalf("logout: %d %s", status, data)
	}
	if _, err := service.Refresh(context.Background(), next.RefreshToken); err != ErrToken {
		t.Fatalf("logged out token accepted: %v", err)
	}
	// Expired refresh tokens must be rejected by the database-backed rotation.
	expiredRaw, expired, err := newSession()
	if err != nil {
		t.Fatal(err)
	}
	expired.UserID = u.ID
	expired.ExpiresAt = time.Now().UTC().Add(-time.Minute)
	if err := repo.SaveSession(context.Background(), expired); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Refresh(context.Background(), expiredRaw); err != ErrToken {
		t.Fatalf("expired refresh accepted: %v", err)
	}
}
