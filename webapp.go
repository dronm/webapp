// Package webapp provides net/http helpers for route declaration, request
// binding through github.com/dronm/modelbind, request-scoped service
// construction, JSON responses, middleware, and modelbind PostgreSQL CRUD
// helpers.
//
// Subpackages add optional integrations:
//
//	webapp/constants - typed constants, caching and PostgreSQL synchronization
//	webapp/database - PostgreSQL provider initialization
//	webapp/session  - Redis-backed HTTP session resolver
//	webapp/events   - PostgreSQL LISTEN/NOTIFY event server
//	webapp/ws       - WebSocket bridge to registered services
package webapp
