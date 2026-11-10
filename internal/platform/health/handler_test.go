package health

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReadinessReflectsDependencyFailure(t *testing.T) {
	for _, failing := range []bool{false, true} {
		checks := map[string]Check{
			"mysql": func(context.Context) error {
				if failing {
					return errors.New("private connection details")
				}
				return nil
			},
			"redis": func(context.Context) error { return nil },
		}
		response := httptest.NewRecorder()
		Handler(checks).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		wantCode, wantMySQL := http.StatusOK, "ok"
		if failing {
			wantCode, wantMySQL = http.StatusServiceUnavailable, "unavailable"
		}
		if response.Code != wantCode {
			t.Fatalf("status = %d, want %d", response.Code, wantCode)
		}
		var body map[string]string
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body["mysql"] != wantMySQL || body["redis"] != "ok" {
			t.Fatalf("unexpected readiness: %v", body)
		}
		if strings.Contains(response.Body.String(), "private") {
			t.Fatal("readiness leaked connection details")
		}
	}
}

func TestLivenessDoesNotDependOnDatabase(t *testing.T) {
	response := httptest.NewRecorder()
	Handler(map[string]Check{
		"mysql": func(context.Context) error {
			t.Fatal("liveness must not call database")
			return nil
		},
	}).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
}
