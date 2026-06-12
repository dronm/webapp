package webapp

import "net/http"

// Group collects routes under one URL prefix and shared middleware.
type Group struct {
	Prefix     string
	Middleware []Middleware
	Routes     *[]Route
}

func NewGroup(prefix string, routes *[]Route, middlewares ...Middleware) Group {
	return Group{
		Prefix:     prefix,
		Routes:     routes,
		Middleware: middlewares,
	}
}

func (g Group) Group(prefix string, middlewares ...Middleware) Group {
	mergedMiddleware := make([]Middleware, 0, len(g.Middleware)+len(middlewares))
	mergedMiddleware = append(mergedMiddleware, g.Middleware...)
	mergedMiddleware = append(mergedMiddleware, middlewares...)

	return Group{
		Prefix:     g.Prefix + prefix,
		Routes:     g.Routes,
		Middleware: mergedMiddleware,
	}
}

func (g Group) Handle(route Route) {
	route.Pattern = g.Prefix + route.Pattern
	route.Middleware = append(append([]Middleware(nil), g.Middleware...), route.Middleware...)
	*g.Routes = append(*g.Routes, route)
}

func (g Group) Route(method string, pattern string, options ...RouteOption) {
	g.Handle(NewRoute(method, pattern, options...))
}

func (g Group) GET(pattern string, options ...RouteOption) {
	g.Route(http.MethodGet, pattern, options...)
}

func (g Group) POST(pattern string, options ...RouteOption) {
	g.Route(http.MethodPost, pattern, options...)
}

func (g Group) PUT(pattern string, options ...RouteOption) {
	g.Route(http.MethodPut, pattern, options...)
}

func (g Group) PATCH(pattern string, options ...RouteOption) {
	g.Route(http.MethodPatch, pattern, options...)
}

func (g Group) DELETE(pattern string, options ...RouteOption) {
	g.Route(http.MethodDelete, pattern, options...)
}
