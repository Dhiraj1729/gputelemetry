package collector

import (
	"context"
	"net/http"
	"time"
)

type Readiness interface{ Ready(context.Context) error }

// HealthHandler separates process liveness from dependency readiness.
func HealthHandler(lifetime context.Context, db Readiness) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok\n")) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		check, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		if lifetime.Err() != nil || db.Ready(check) != nil {
			http.Error(w, "database unavailable or migrations missing", 503)
			return
		}
		w.Write([]byte("ready\n"))
	})
	return mux
}
