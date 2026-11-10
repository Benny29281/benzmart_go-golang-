package health

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

type Check func(context.Context) error

func Handler(checks map[string]Check) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		respond(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		type result struct {
			name string
			err  error
		}
		results := make(chan result, len(checks))
		for name, check := range checks {
			go func(name string, check Check) { results <- result{name, check(ctx)} }(name, check)
		}
		status := http.StatusOK
		dependencies := make(map[string]string, len(checks))
		for range checks {
			select {
			case res := <-results:
				dependencies[res.name] = "ok"
				if res.err != nil {
					status = http.StatusServiceUnavailable
					dependencies[res.name] = "unavailable"
				}
			case <-ctx.Done():
				for name := range checks {
					if _, ok := dependencies[name]; !ok {
						dependencies[name] = "unavailable"
					}
				}
				respond(w, http.StatusServiceUnavailable, dependencies)
				return
			}
		}
		respond(w, status, dependencies)
	})
	return mux
}

func respond(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
