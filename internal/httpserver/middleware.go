package httpserver

import (
	"log/slog"
	"net/http"
	"time"
)

// statusWriter records the response status code for logging.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// requestLog logs every request with method, path, status and duration.
// Health probes stay at debug level to keep production logs readable.
func requestLog(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)

		attrs := []any{
			"method", r.Method,
			"path", r.URL.Path,
			"status", sw.status,
			"duration_ms", float64(time.Since(start).Microseconds()) / 1000.0,
			"remote", r.RemoteAddr,
		}
		if r.URL.Path == "/healthz" {
			logger.Debug("http_request", attrs...)
		} else {
			logger.Info("http_request", attrs...)
		}
	})
}

// recoverMiddleware converts handler panics into 500 responses and logs
// the panic with request context. It never lets a panic kill the process.
func recoverMiddleware(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				logger.Error("http_panic",
					"method", r.Method,
					"path", r.URL.Path,
					"panic", rec,
				)
				// Best effort: headers may already be on the wire.
				writeJSON(w, http.StatusInternalServerError, map[string]string{
					"error": "internal server error",
				})
			}
		}()
		next.ServeHTTP(w, r)
	})
}
