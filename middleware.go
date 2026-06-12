package webapp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/dronm/session"
)

type Middleware func(http.Handler) http.Handler

type SessionResolver func(w http.ResponseWriter, r *http.Request) (session.Session, error)

type PermissionChecker func(r *http.Request, permission string) error

func Chain(middlewares ...Middleware) Middleware {
	return func(next http.Handler) http.Handler {
		for i := len(middlewares) - 1; i >= 0; i-- {
			if middlewares[i] == nil {
				continue
			}
			next = middlewares[i](next)
		}

		return next
	}
}

func chain(middlewares ...Middleware) Middleware {
	return Chain(middlewares...)
}

// SessionMiddleware resolves a session and stores it in the request context.
// Place CORS middleware before this middleware, so OPTIONS preflight requests do
// not create sessions.
func SessionMiddleware(resolver SessionResolver) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if resolver == nil {
				next.ServeHTTP(w, r)
				return
			}

			sess, err := resolver(w, r)
			if err != nil {
				WriteServiceError(w, err)
				return
			}

			ctx := ContextWithSession(r.Context(), sess)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// QueryIDMiddleware reads a query/request ID from a header and stores it in the
// request context. If the header is absent, it generates a random ID.
func QueryIDMiddleware(headerName string) Middleware {
	if headerName == "" {
		headerName = DefaultQueryIDHeader
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			queryID := r.Header.Get(headerName)
			if queryID == "" {
				queryID = newRequestID()
			}

			w.Header().Set(headerName, queryID)
			ctx := ContextWithQueryID(r.Context(), queryID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// PermissionMiddleware runs a route permission check before the handler.
func PermissionMiddleware(permission string, checker PermissionChecker) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if permission == "" {
				next.ServeHTTP(w, r)
				return
			}

			if checker == nil {
				WriteJSONError(
					w,
					http.StatusInternalServerError,
					"permission_checker_missing",
					"Permission checker is not configured",
					map[string]any{
						"permission": permission,
					},
				)
				return
			}

			if err := checker(r, permission); err != nil {
				WriteServiceError(w, err)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// CheckPermission is an alias that reads better in route definitions.
func CheckPermission(permission string, checker PermissionChecker) Middleware {
	return PermissionMiddleware(permission, checker)
}

type CORSConfig struct {
	AllowedOrigins   []string
	AllowedMethods   []string
	AllowedHeaders   []string
	ExposedHeaders   []string
	AllowCredentials bool
	MaxAgeSeconds    int
}

func CORSMiddleware(cfg CORSConfig) Middleware {
	allowedMethods := strings.Join(defaultStrings(cfg.AllowedMethods, []string{
		http.MethodGet,
		http.MethodPost,
		http.MethodPut,
		http.MethodPatch,
		http.MethodDelete,
		http.MethodOptions,
	}), ", ")

	allowedHeaders := strings.Join(defaultStrings(cfg.AllowedHeaders, []string{
		"Content-Type",
		"Authorization",
		"Accept",
		"Origin",
		"X-Requested-With",
		DefaultQueryIDHeader,
	}), ", ")

	exposedHeaders := strings.Join(defaultStrings(cfg.ExposedHeaders, []string{
		"Content-Length",
		"Content-Type",
		DefaultQueryIDHeader,
	}), ", ")

	originSet := make(map[string]struct{}, len(cfg.AllowedOrigins))
	allowAnyOrigin := false
	for _, origin := range cfg.AllowedOrigins {
		if origin == "*" {
			allowAnyOrigin = true
			continue
		}
		originSet[origin] = struct{}{}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin != "" {
				if allowAnyOrigin && !cfg.AllowCredentials {
					w.Header().Set("Access-Control-Allow-Origin", "*")
				} else if _, ok := originSet[origin]; ok {
					w.Header().Set("Access-Control-Allow-Origin", origin)
					w.Header().Add("Vary", "Origin")
				}
			}

			w.Header().Set("Access-Control-Allow-Methods", allowedMethods)
			w.Header().Set("Access-Control-Allow-Headers", allowedHeaders)
			w.Header().Set("Access-Control-Expose-Headers", exposedHeaders)
			if cfg.AllowCredentials {
				w.Header().Set("Access-Control-Allow-Credentials", "true")
			}
			if cfg.MaxAgeSeconds > 0 {
				w.Header().Set("Access-Control-Max-Age", strconv.Itoa(cfg.MaxAgeSeconds))
			}

			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

func TimeoutMiddleware(timeout time.Duration) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if timeout <= 0 {
				next.ServeHTTP(w, r)
				return
			}

			ctx, cancel := context.WithTimeout(r.Context(), timeout)
			defer cancel()

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func RequestLoggerMiddleware() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			startedAt := time.Now()
			rw := &statusRecorder{ResponseWriter: w, statusCode: http.StatusOK}
			next.ServeHTTP(rw, r)
			slog.Info(
				"http request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", rw.statusCode,
				"duration", time.Since(startedAt),
				"remote_addr", r.RemoteAddr,
				"query_id", QueryIDFromContext(r.Context()),
			)
		})
	}
}

func RecoverMiddleware() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					slog.Error(
						"panic recovered",
						"panic", rec,
						"method", r.Method,
						"path", r.URL.Path,
						"query_id", QueryIDFromContext(r.Context()),
						"stack", string(debug.Stack()),
					)

					WriteJSONError(
						w,
						http.StatusInternalServerError,
						"internal_error",
						"Internal server error",
						nil,
					)
				}
			}()

			next.ServeHTTP(w, r)
		})
	}
}

type statusRecorder struct {
	http.ResponseWriter
	statusCode int
}

func (r *statusRecorder) WriteHeader(statusCode int) {
	r.statusCode = statusCode
	r.ResponseWriter.WriteHeader(statusCode)
}

func defaultStrings(value []string, fallback []string) []string {
	if len(value) == 0 {
		return fallback
	}

	return value
}

func newRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}

	return hex.EncodeToString(b[:])
}

