package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

type Limiter interface {
	Allow(context.Context, string, int) (bool, error)
}

type RedisLimiter struct{ Client *redis.Client }

var limitScript = redis.NewScript(`
local current = redis.call('INCR', KEYS[1])
if current == 1 then redis.call('EXPIRE', KEYS[1], 60) end
return current
`)

func (l RedisLimiter) Allow(ctx context.Context, key string, limit int) (bool, error) {
	count, err := limitScript.Run(ctx, l.Client, []string{"auth:limit:" + key}).Int64()
	return count <= int64(limit), err
}

type userKey struct{}
type correlationKey struct{}

type Handler struct {
	Service *Service
	Limiter Limiter
	Logger  *slog.Logger
}

func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/auth/register", h.register)
	mux.HandleFunc("POST /api/v1/auth/login", h.login)
	mux.HandleFunc("POST /api/v1/auth/refresh", h.refresh)
	mux.Handle("POST /api/v1/auth/logout", h.require("", http.HandlerFunc(h.logout)))
	mux.Handle("GET /api/v1/auth/me", h.require("", http.HandlerFunc(h.me)))
	mux.Handle("GET /api/v1/admin/me", h.require("admin", http.HandlerFunc(h.me)))
	return mux
}

// Correlation creates trusted request IDs instead of reflecting untrusted headers.
func Correlation(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := uuid.NewString()
		w.Header().Set("X-Correlation-ID", id)
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		ctx = context.WithValue(ctx, correlationKey{}, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func respond(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func (h *Handler) fail(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	id, _ := r.Context().Value(correlationKey{}).(string)
	respond(w, status, map[string]any{"error": map[string]string{"code": code, "message": message, "correlation_id": id}})
}

func (h *Handler) failure(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrInvalid):
		h.fail(w, r, 400, "VALIDATION_ERROR", "Nama wajib diisi (maksimal 100 karakter), email harus valid, dan password 8–72 byte")
	case errors.Is(err, ErrDuplicate):
		h.fail(w, r, 409, "EMAIL_EXISTS", "Email sudah terdaftar")
	case errors.Is(err, ErrCredentials):
		h.fail(w, r, 401, "UNAUTHORIZED", "Email atau password salah")
	case errors.Is(err, ErrToken):
		h.fail(w, r, 401, "UNAUTHORIZED", "Token tidak valid atau kedaluwarsa")
	default:
		h.Logger.Error("auth request gagal", "error", err, "correlation_id", r.Context().Value(correlationKey{}))
		h.fail(w, r, 503, "SERVICE_UNAVAILABLE", "Layanan sementara tidak tersedia")
	}
}

func (h *Handler) decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 16384)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(v); err != nil {
		h.fail(w, r, 400, "VALIDATION_ERROR", "Body harus berupa JSON valid dengan field yang sesuai")
		return false
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		h.fail(w, r, 400, "VALIDATION_ERROR", "Body hanya boleh berisi satu objek JSON")
		return false
	}
	return true
}

func (h *Handler) limit(w http.ResponseWriter, r *http.Request, scope, identity string, count int) bool {
	// Only trust the direct peer address. Proxy headers need explicit trusted-proxy configuration.
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		ip = r.RemoteAddr
	}
	hash := sha256.Sum256([]byte(scope + ":" + ip + ":" + strings.ToLower(strings.TrimSpace(identity))))
	allowed, err := h.Limiter.Allow(r.Context(), hex.EncodeToString(hash[:]), count)
	if err != nil {
		h.failure(w, r, err)
		return false
	}
	if !allowed {
		w.Header().Set("Retry-After", "60")
		h.fail(w, r, 429, "RATE_LIMITED", "Terlalu banyak percobaan. Coba lagi dalam satu menit")
		return false
	}
	return true
}

func (h *Handler) register(w http.ResponseWriter, r *http.Request) {
	if !h.limit(w, r, "register", "", 10) {
		return
	}
	var input struct {
		Name     string `json:"name"`
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !h.decode(w, r, &input) {
		return
	}
	u, err := h.Service.Register(r.Context(), input.Name, input.Email, input.Password, "customer")
	if err != nil {
		h.failure(w, r, err)
		return
	}
	respond(w, http.StatusCreated, map[string]any{"user": u})
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	if !h.limit(w, r, "login-ip", "", 30) {
		return
	}
	var input struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !h.decode(w, r, &input) {
		return
	}
	if !h.limit(w, r, "login-email", input.Email, 5) {
		return
	}
	tokens, err := h.Service.Login(r.Context(), input.Email, input.Password)
	if err != nil {
		h.failure(w, r, err)
		return
	}
	respond(w, http.StatusOK, tokens)
}

func (h *Handler) refresh(w http.ResponseWriter, r *http.Request) {
	if !h.limit(w, r, "refresh", "", 30) {
		return
	}
	var input struct {
		RefreshToken string `json:"refresh_token"`
	}
	if !h.decode(w, r, &input) {
		return
	}
	tokens, err := h.Service.Refresh(r.Context(), input.RefreshToken)
	if err != nil {
		h.failure(w, r, err)
		return
	}
	respond(w, http.StatusOK, tokens)
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	var input struct {
		RefreshToken string `json:"refresh_token"`
	}
	if !h.decode(w, r, &input) {
		return
	}
	u := r.Context().Value(userKey{}).(User)
	if err := h.Service.Logout(r.Context(), u.ID, input.RefreshToken); err != nil {
		h.failure(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	respond(w, http.StatusOK, map[string]any{"user": r.Context().Value(userKey{}).(User)})
}

func (h *Handler) require(role string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Fields(r.Header.Get("Authorization"))
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			h.failure(w, r, ErrToken)
			return
		}
		u, err := h.Service.Authenticate(r.Context(), parts[1])
		if err != nil {
			h.failure(w, r, err)
			return
		}
		if role != "" && u.Role != role {
			h.fail(w, r, 403, "FORBIDDEN", "Akses hanya untuk admin")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey{}, u)))
	})
}
