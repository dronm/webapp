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
	if route.Handler != nil {
		return route.Handler
	}

	return func(w http.ResponseWriter, r *http.Request) {
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
		DB:             appCtx.DB,
		EventPublisher: appCtx.EventPublisher,
		Session:        SessionFromContext(r.Context()),
		QueryID:        queryID,
	}
}

func ContextWithSession(ctx context.Context, sess session.Session) context.Context {
	return context.WithValue(ctx, sessionContextKey, sess)
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
