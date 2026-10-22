package constants

import (
	"context"
	"encoding/json"
	"reflect"
)

// Constant is a project-specific typed accessor backed by the same manager as
// the generic API. It owns no independent cache. Returned values are freshly
// decoded, so mutating maps, slices or pointers cannot change cached settings.
type Constant[T any] struct {
	manager *Manager
	code    string
}

func NewConstant[T any](manager *Manager, code string) (*Constant[T], error) {
	if manager == nil {
		return nil, constantError(ErrDefinition, 0, code, "manager is nil")
	}
	definition, ok := manager.definition(code)
	if !ok {
		return nil, constantError(ErrDefinition, 0, code, "code has no registered definition")
	}
	if definition.goType != reflect.TypeFor[T]() {
		return nil, constantError(ErrDefinition, 0, code, "accessor Go type does not match the registered definition")
	}
	return &Constant[T]{manager: manager, code: code}, nil
}

func (c *Constant[T]) Code() string {
	if c == nil {
		return ""
	}
	return c.code
}

func (c *Constant[T]) GetValue(ctx context.Context) (T, error) {
	value, _, err := c.GetVersioned(ctx)
	return value, err
}

// GetVersioned returns a fresh typed value and the version needed for an
// optimistic SetValueIfVersion call.
func (c *Constant[T]) GetVersioned(ctx context.Context) (T, int64, error) {
	var zero T
	if c == nil || c.manager == nil {
		return zero, 0, constantError(ErrDefinition, 0, "", "typed accessor is nil")
	}
	values, err := c.manager.GetByCodes(ctx, []string{c.code})
	if err != nil {
		return zero, 0, err
	}
	if len(values) != 1 {
		return zero, 0, constantError(ErrNotFound, 0, c.code, "constant is missing")
	}
	value, err := decodeTyped[T](values[0].Value)
	if err != nil {
		return zero, 0, &Error{Kind: ErrValidation, ID: values[0].ID, Code: c.code, Message: "cached value could not be decoded", Cause: err}
	}
	return value, values[0].Version, nil
}

func (c *Constant[T]) SetValue(ctx context.Context, value T) error {
	return c.set(ctx, value, nil)
}

func (c *Constant[T]) SetValueIfVersion(ctx context.Context, value T, version int64) error {
	if version <= 0 {
		return constantError(ErrValidation, 0, c.Code(), "expected version must be positive")
	}
	return c.set(ctx, value, &version)
}

func (c *Constant[T]) set(ctx context.Context, value T, expectedVersion *int64) error {
	if c == nil || c.manager == nil {
		return constantError(ErrDefinition, 0, "", "typed accessor is nil")
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return &Error{Kind: ErrValidation, Code: c.code, Message: "value could not be encoded as JSON", Cause: err}
	}
	_, err = c.manager.SetByCode(ctx, c.code, raw, expectedVersion)
	return err
}
