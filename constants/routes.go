package constants

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/dronm/webapp"
)

const maxRequestBody = 1 << 20

// Routes creates GET prefix, GET prefix/{id}, POST prefix/get, and POST
// prefix/set. RegisterService must use the same serviceName. Authentication
// middleware should populate the request context; the service Authorizer is
// mandatory and also applies to direct WebSocket service calls.
func Routes(prefix, serviceName string) ([]webapp.Route, error) {
	prefix = strings.TrimRight(prefix, "/")
	if prefix == "" || prefix[0] != '/' || strings.ContainsAny(prefix, "{}?# \t\r\n") || strings.Contains(prefix, "//") {
		return nil, fmt.Errorf("constants routes: prefix must be a non-root absolute path without wildcards or whitespace")
	}
	if strings.TrimSpace(serviceName) == "" {
		return nil, fmt.Errorf("constants routes: service name is empty")
	}
	return []webapp.Route{
		webapp.GET(prefix, webapp.WithService(serviceName, "List"), webapp.WithBinder(listBinder())),
		webapp.GET(prefix+"/{id}", webapp.WithService(serviceName, "Detail"), webapp.WithBinder(detailBinder())),
		webapp.POST(prefix+"/get", webapp.WithService(serviceName, "Get"), webapp.WithBinder(jsonBinder[GetInput]())),
		webapp.POST(prefix+"/set", webapp.WithService(serviceName, "Set"), webapp.WithBinder(jsonBinder[SetInput]())),
	}, nil
}

func listBinder() webapp.Binder {
	return func(r *http.Request) (any, error) {
		input := ListOptions{}
		for name, values := range r.URL.Query() {
			if len(values) != 1 {
				return nil, webapp.BadRequest("Query parameters must occur once", map[string]any{"field": name})
			}
			switch name {
			case "search":
				input.Search = values[0]
			case "value_type":
				input.ValueType = ValueType(values[0])
			case "limit", "offset":
				value, err := strconv.Atoi(values[0])
				if err != nil {
					return nil, webapp.BadRequest("An integer query parameter is required", map[string]any{"field": name})
				}
				if name == "limit" {
					input.Limit = value
				} else {
					input.Offset = value
				}
			default:
				return nil, webapp.BadRequest("Unknown query parameter", map[string]any{"field": name})
			}
		}
		return input, nil
	}
}

func detailBinder() webapp.Binder {
	return func(r *http.Request) (any, error) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id <= 0 {
			return nil, webapp.BadRequest("A positive constant id is required", nil)
		}
		return DetailInput{ID: id}, nil
	}
}

func jsonBinder[T any]() webapp.Binder {
	return func(r *http.Request) (any, error) {
		if r.Body == nil {
			return nil, webapp.BadRequest("A JSON object is required", nil)
		}
		raw, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody+1))
		if err != nil {
			return nil, webapp.BadRequest("The request body could not be read", nil)
		}
		if len(raw) > maxRequestBody {
			return nil, webapp.NewHTTPError(http.StatusRequestEntityTooLarge, "request_too_large", "The constants request is too large", nil)
		}
		return decodeServiceInput[T](raw)
	}
}

func serviceInputDecoder[T any]() webapp.ServiceInputDecoder {
	return func(_ context.Context, raw json.RawMessage) (any, error) {
		if len(raw) > maxRequestBody {
			return nil, webapp.NewHTTPError(http.StatusRequestEntityTooLarge, "request_too_large", "The constants request is too large", nil)
		}
		return decodeServiceInput[T](raw)
	}
}

func decodeServiceInput[T any](raw []byte) (T, error) {
	var value T
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return value, webapp.BadRequest("A JSON object is required", nil)
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return value, webapp.BadRequest("Invalid constants request", nil)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return value, webapp.BadRequest("Exactly one JSON object is required", nil)
	}
	return value, nil
}
