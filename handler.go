package webapp

import (
	"context"
	"net/http"

	"github.com/dronm/session"
)

type contextKey string

const (
	sessionContextKey contextKey = "webapp.session"
	queryIDContextKey contextKey = "webapp.query_id"
)

func BuildHandler(route Route, appCtx *AppContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if appCtx != nil && appCtx.Access != nil {
			r = r.WithContext(ContextWithAccess(r.Context(), appCtx.Access))
		}
		if route.Handler != nil {
			route.Handler(w, r)
			return
		}
		var input any
		var err error

		if route.Binder != nil {
			input, err = route.Binder(r)
			if err != nil {
				WriteBindingError(w, err)
				return
			}
		}

		svcCtx := NewServiceContext(r, appCtx)

		result, err := CallServiceMethod(
			r.Context(),
			route.ServiceName,
			route.ServiceFunc,
			input,
			svcCtx,
		)
		if err != nil {
			WriteServiceError(w, err)
			return
		}

		statusCode := route.SuccessCode
		if statusCode == 0 {
			statusCode = http.StatusOK
		}

		_ = WriteJSON(w, statusCode, result)
	}
}

func NewServiceContext(r *http.Request, appCtx *AppContext) ServiceContext {
	if appCtx == nil {
		appCtx = &AppContext{}
	}

	queryID := QueryIDFromContext(r.Context())
	if queryID == "" {
		headerName := appCtx.QueryIDHeader
		if headerName == "" {
			headerName = DefaultQueryIDHeader
		}
		queryID = r.Header.Get(headerName)
	}

	return ServiceContext{
		Access:         appCtx.Access,
		DB:             appCtx.DB,
		EventPublisher: appCtx.EventPublisher,
		Session:        SessionFromContext(r.Context()),
		QueryID:        queryID,
	}
}

func ContextWithSession(ctx context.Context, sess session.Session) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = context.WithValue(ctx, sessionContextKey, sess)
	// Replacing a session invalidates both resolved and explicitly supplied
	// identities. A WebSocket message must not inherit the upgrade identity.
	ctx = context.WithValue(ctx, principalContextKey, struct{}{})
	if state := accessStateFromContext(ctx); state != nil {
		ctx = ContextWithAccess(ctx, state.controller)
	}
	return ctx
}

func SessionFromContext(ctx context.Context) session.Session {
	if ctx == nil {
		return nil
	}

	sess, _ := ctx.Value(sessionContextKey).(session.Session)
	return sess
}

func ContextWithQueryID(ctx context.Context, queryID string) context.Context {
	return context.WithValue(ctx, queryIDContextKey, queryID)
}

func QueryIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}

	queryID, _ := ctx.Value(queryIDContextKey).(string)
	return queryID
}
