package webapp

import (
	"fmt"
	"net/http"
	"time"

	"github.com/dronm/ds/v4"
)

const (
	DefaultQueryIDHeader = "Query-Id"
)

// Route describes one HTTP endpoint and the service method called by it.
type Route struct {
	Name       string
	Method     string
	Permission string
	Pattern    string
	Middleware []Middleware

	Timeout time.Duration

	Binder Binder

	ServiceName string
	ServiceFunc string

	Handler     http.HandlerFunc
	SuccessCode int
}

// AppContext contains application-wide dependencies available to all handlers.
type AppContext struct {
	DB ds.Provider

	// QueryIDHeader defaults to Query-Id when empty.
	QueryIDHeader string

	// DefaultTimeout is applied to routes that do not define their own timeout.
	// Zero means no default route timeout.
	DefaultTimeout time.Duration

	// PermissionChecker is used by routes configured with WithPermission.
	PermissionChecker PermissionChecker
}

// RouteOption mutates a route during route construction.
type RouteOption func(route *Route)

func NewRoute(method string, pattern string, options ...RouteOption) Route {
	route := Route{
		Method:  method,
		Pattern: pattern,
	}

	for _, option := range options {
		if option != nil {
			option(&route)
		}
	}

	return route
}

func GET(pattern string, options ...RouteOption) Route {
	return NewRoute(http.MethodGet, pattern, options...)
}

func POST(pattern string, options ...RouteOption) Route {
	return NewRoute(http.MethodPost, pattern, options...)
}

func PUT(pattern string, options ...RouteOption) Route {
	return NewRoute(http.MethodPut, pattern, options...)
}

func PATCH(pattern string, options ...RouteOption) Route {
	return NewRoute(http.MethodPatch, pattern, options...)
}

func DELETE(pattern string, options ...RouteOption) Route {
	return NewRoute(http.MethodDelete, pattern, options...)
}

func WithName(name string) RouteOption {
	return func(route *Route) {
		route.Name = name
	}
}

func WithPermission(permission string) RouteOption {
	return func(route *Route) {
		route.Permission = permission
	}
}

func WithMiddleware(middlewares ...Middleware) RouteOption {
	return func(route *Route) {
		route.Middleware = append(route.Middleware, middlewares...)
	}
}

func WithBinder(binder Binder) RouteOption {
	return func(route *Route) {
		route.Binder = binder
	}
}

func WithService(serviceName string, serviceFunc string) RouteOption {
	return func(route *Route) {
		route.ServiceName = serviceName
		route.ServiceFunc = serviceFunc
	}
}

func WithHandler(handler http.HandlerFunc) RouteOption {
	return func(route *Route) {
		route.Handler = handler
	}
}

func WithSuccessCode(statusCode int) RouteOption {
	return func(route *Route) {
		route.SuccessCode = statusCode
	}
}

func WithTimeout(timeout time.Duration) RouteOption {
	return func(route *Route) {
		route.Timeout = timeout
	}
}

// BuildRouter creates a net/http ServeMux. Patterns use Go 1.22+ ServeMux
// syntax, for example: GET /supplier/{id}.
func BuildRouter(appCtx *AppContext, routes []Route, globalMiddleware ...Middleware) http.Handler {
	mux := http.NewServeMux()

	for _, route := range routes {
		handler := BuildHandler(route, appCtx)

		routeMiddleware := make([]Middleware, 0, 2+len(route.Middleware))

		timeout := route.Timeout
		if timeout <= 0 && appCtx != nil {
			timeout = appCtx.DefaultTimeout
		}
		if timeout > 0 {
			routeMiddleware = append(routeMiddleware, TimeoutMiddleware(timeout))
		}

		if route.Permission != "" {
			routeMiddleware = append(
				routeMiddleware,
				PermissionMiddleware(route.Permission, appCtx.PermissionChecker),
			)
		}

		routeMiddleware = append(routeMiddleware, route.Middleware...)

		pattern := route.Pattern
		if route.Method != "" {
			pattern = route.Method + " " + route.Pattern
		}

		mux.Handle(pattern, Chain(routeMiddleware...)(handler))
	}

	return Chain(globalMiddleware...)(mux)
}

// MustBuildRouter is the same as BuildRouter, but validates route definitions
// first and panics on configuration errors. It is intended for startup code.
func MustBuildRouter(appCtx *AppContext, routes []Route, globalMiddleware ...Middleware) http.Handler {
	if err := ValidateRoutes(routes); err != nil {
		panic(err)
	}

	for i, route := range routes {
		if route.Permission != "" && (appCtx == nil || appCtx.PermissionChecker == nil) {
			panic(fmt.Errorf(
				"route %s requires permission %q but permission checker is not configured",
				routeLabel(i, route),
				route.Permission,
			))
		}
	}

	return BuildRouter(appCtx, routes, globalMiddleware...)
}

func ValidateRoutes(routes []Route) error {
	for i, route := range routes {
		if route.Pattern == "" {
			return fmt.Errorf("route %d pattern is empty", i)
		}

		if route.Handler == nil {
			if route.ServiceName == "" {
				return fmt.Errorf("route %s service name is empty", routeLabel(i, route))
			}
			if route.ServiceFunc == "" {
				return fmt.Errorf("route %s service func is empty", routeLabel(i, route))
			}
		}
	}

	return nil
}

func routeLabel(index int, route Route) string {
	if route.Name != "" {
		return route.Name
	}

	if route.Method != "" || route.Pattern != "" {
		return fmt.Sprintf("%s %s", route.Method, route.Pattern)
	}

	return fmt.Sprintf("#%d", index)
}
