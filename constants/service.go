package constants

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/dronm/webapp"
)

type Operation string

const (
	OperationList   Operation = "list"
	OperationDetail Operation = "detail"
	OperationGet    Operation = "get"
	OperationSet    Operation = "set"
)

// Authorizer checks a transport request before the manager is accessed. List
// requires a catalogue-wide metadata grant (ids is nil); other operations pass
// every requested ID. The callback must return an error to deny access. Use
// webapp.Unauthorized/Forbidden when the client should receive a specific status.
// The shared cache does not carry a user's authorization decision.
type Authorizer func(ctx context.Context, operation Operation, ids []int64) error

type ListOptions struct {
	Search    string    `json:"search,omitempty"`
	ValueType ValueType `json:"value_type,omitempty"`
	Limit     int       `json:"limit,omitempty"`
	Offset    int       `json:"offset,omitempty"`
}

type ListResult struct {
	Items []Metadata `json:"items"`
	Total int        `json:"total"`
}

type DetailInput struct {
	ID int64 `json:"id"`
}

type GetInput struct {
	IDs []int64 `json:"ids"`
}

type SetInput struct {
	Items []Change `json:"items"`
}

type serviceManager interface {
	List(context.Context, ListOptions) (ListResult, error)
	Detail(context.Context, int64) (Metadata, error)
	Get(context.Context, []int64) ([]Value, error)
	Set(context.Context, []Change) ([]Value, error)
}

// Service exposes the administration API. Use one process-wide Manager and
// instantiate lightweight services around it; never create a manager per request.
type Service struct {
	manager   serviceManager
	authorize Authorizer
}

func NewService(manager *Manager, authorize Authorizer) (*Service, error) {
	if manager == nil {
		return nil, fmt.Errorf("constants service: manager is nil")
	}
	if authorize == nil {
		return nil, fmt.Errorf("constants service: authorizer is required")
	}
	return &Service{manager: manager, authorize: authorize}, nil
}

// RegisterService registers only List, Detail, Get and Set. Every request uses
// the same manager. Authorization lives in the service, so WebSocket callers
// receive the same checks as HTTP callers. No value-bearing event is broadcast.
func RegisterService(name string, manager *Manager, authorize Authorizer) error {
	service, err := NewService(manager, authorize)
	if err != nil {
		return err
	}
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("constants service: name is empty")
	}
	return webapp.RegisterService(name, service,
		func(webapp.ServiceContext) any {
			return &Service{manager: manager, authorize: authorize}
		},
		webapp.WithMethodInputDecoder("List", serviceInputDecoder[ListOptions]()),
		webapp.WithMethodInputDecoder("Detail", serviceInputDecoder[DetailInput]()),
		webapp.WithMethodInputDecoder("Get", serviceInputDecoder[GetInput]()),
		webapp.WithMethodInputDecoder("Set", serviceInputDecoder[SetInput]()),
	)
}

func (s *Service) List(ctx context.Context, input ListOptions) (ListResult, error) {
	if err := s.check(ctx, OperationList, nil); err != nil {
		return ListResult{}, err
	}
	input, err := normalizeListOptions(input)
	if err != nil {
		return ListResult{}, transportError(err)
	}
	result, err := s.manager.List(ctx, input)
	return result, transportError(err)
}

func (s *Service) Detail(ctx context.Context, input DetailInput) (Metadata, error) {
	if err := s.check(ctx, OperationDetail, []int64{input.ID}); err != nil {
		return Metadata{}, err
	}
	if input.ID <= 0 {
		return Metadata{}, webapp.BadRequest("A positive constant id is required", nil)
	}
	result, err := s.manager.Detail(ctx, input.ID)
	return result, transportError(err)
}

func (s *Service) Get(ctx context.Context, input GetInput) ([]Value, error) {
	if err := s.check(ctx, OperationGet, input.IDs); err != nil {
		return nil, err
	}
	if err := validateServiceIDs(input.IDs); err != nil {
		return nil, err
	}
	result, err := s.manager.Get(ctx, input.IDs)
	return result, transportError(err)
}

func (s *Service) Set(ctx context.Context, input SetInput) ([]Value, error) {
	ids := make([]int64, len(input.Items))
	for i := range input.Items {
		ids[i] = input.Items[i].ID
	}
	if err := s.check(ctx, OperationSet, ids); err != nil {
		return nil, err
	}
	if err := validateServiceIDs(ids); err != nil {
		return nil, err
	}
	seen := make(map[int64]bool, len(ids))
	for _, item := range input.Items {
		if seen[item.ID] {
			return nil, webapp.BadRequest("A constant may occur only once in a set batch", map[string]any{"id": item.ID})
		}
		seen[item.ID] = true
		if len(item.Value) == 0 || !json.Valid(item.Value) {
			return nil, webapp.BadRequest("Each assignment requires a JSON value", map[string]any{"id": item.ID})
		}
		if item.ExpectedVersion != nil && *item.ExpectedVersion <= 0 {
			return nil, webapp.BadRequest("expected_version must be positive", map[string]any{"id": item.ID})
		}
	}
	result, err := s.manager.Set(ctx, input.Items)
	return result, transportError(err)
}

func (s *Service) check(ctx context.Context, operation Operation, ids []int64) error {
	if s == nil || s.manager == nil || s.authorize == nil {
		return webapp.Internal("Constants service is not configured", nil)
	}
	// A project callback may retain or change its input; it must not mutate the
	// IDs that will subsequently be passed to the manager.
	if err := s.authorize(ctx, operation, append([]int64(nil), ids...)); err != nil {
		var httpErr webapp.HTTPError
		if errors.As(err, &httpErr) {
			return err
		}
		return webapp.WrapHTTPError(http.StatusForbidden, "forbidden", "Access to constants is denied", nil, err)
	}
	return nil
}

func normalizeListOptions(input ListOptions) (ListOptions, error) {
	if input.Limit == 0 {
		input.Limit = 100
	}
	if input.Limit < 1 || input.Limit > 500 || input.Offset < 0 {
		return ListOptions{}, constantError(ErrValidation, 0, "", "limit must be between 1 and 500 and offset must be nonnegative")
	}
	switch input.ValueType {
	case "", TypeBool, TypeInt64, TypeFloat64, TypeString, TypeDecimal, TypeJSON:
	default:
		return ListOptions{}, constantError(ErrValidation, 0, "", "unknown value_type")
	}
	input.Search = strings.TrimSpace(input.Search)
	return input, nil
}

func validateServiceIDs(ids []int64) error {
	if len(ids) == 0 || len(ids) > MaxBatchSize {
		return webapp.BadRequest(fmt.Sprintf("Between 1 and %d constant ids are required", MaxBatchSize), nil)
	}
	for _, id := range ids {
		if id <= 0 {
			return webapp.BadRequest("Constant ids must be positive", nil)
		}
	}
	return nil
}

func transportError(err error) error {
	if err == nil {
		return nil
	}
	var httpErr webapp.HTTPError
	if errors.As(err, &httpErr) {
		return err
	}
	var detail *Error
	var details any
	if errors.As(err, &detail) {
		identity := map[string]any{}
		if detail.ID != 0 {
			identity["id"] = detail.ID
		}
		if detail.Code != "" {
			identity["code"] = detail.Code
		}
		if len(identity) != 0 {
			details = identity
		}
	}
	switch {
	case errors.Is(err, ErrNotStarted), errors.Is(err, ErrClosed), errors.Is(err, ErrUnsynced):
		return webapp.WrapHTTPError(http.StatusServiceUnavailable, "constants_unavailable", "Constants are temporarily unavailable", nil, err)
	case errors.Is(err, ErrNotFound):
		return webapp.WrapHTTPError(http.StatusNotFound, "constant_not_found", "Constant was not found", details, err)
	case errors.Is(err, ErrConflict):
		return webapp.WrapHTTPError(http.StatusConflict, "constant_conflict", "The constant changed; reload it before saving", details, err)
	case errors.Is(err, ErrValidation):
		return webapp.WrapHTTPError(http.StatusBadRequest, "constant_validation", "The constant value or request is invalid", details, err)
	default:
		return webapp.WrapHTTPError(http.StatusInternalServerError, "constants_error", "The constants operation could not be completed", nil, err)
	}
}
