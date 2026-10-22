package constants

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
)

// Definition connects a migration-owned constant code to one concrete Go type.
// Validate runs for generic Set calls, typed writes, and database/cache loads.
// Validators should be side-effect free and must not read the same manager.
// Missing required fields in a JSON object should be checked by Validate: Go's
// JSON decoder cannot distinguish an absent scalar field from its zero value.
type Definition[T any] struct {
	Code      string
	ValueType ValueType
	Required  bool
	AllowNull bool
	// Editor is optional JSON metadata for a project's configuration UI.
	Editor   json.RawMessage
	Validate func(context.Context, T) error
}

type registeredDefinition struct {
	code      string
	valueType ValueType
	required  bool
	allowNull bool
	editor    json.RawMessage
	goType    reflect.Type
	check     func(context.Context, json.RawMessage) error
}

func (d registeredDefinition) validate(ctx context.Context, raw json.RawMessage) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := d.check(ctx, raw); err != nil {
		return &Error{Kind: ErrValidation, Code: d.code, Message: err.Error(), Cause: err}
	}
	return nil
}

// Registry is populated during application startup. NewManager takes its own
// immutable snapshot, so subsequent registrations do not change that manager.
type Registry struct {
	mu          sync.RWMutex
	definitions map[string]registeredDefinition
}

func NewRegistry() *Registry {
	return &Registry{definitions: make(map[string]registeredDefinition)}
}

// Register declares a concrete Go type and its validator for a constant code.
// The generic type is inferred from Definition[T]; aliases and pointer types
// are supported. Duplicate codes are rejected rather than silently replaced.
func Register[T any](registry *Registry, definition Definition[T]) error {
	if registry == nil {
		return constantError(ErrDefinition, 0, definition.Code, "registry is nil")
	}
	code := definition.Code
	if code == "" || len(code) > 256 || strings.TrimSpace(code) != code || !utf8.ValidString(code) || strings.ContainsRune(code, '\x00') {
		return constantError(ErrDefinition, 0, code, "code must be 1–256 UTF-8 bytes without surrounding whitespace or NUL")
	}
	typ := reflect.TypeFor[T]()
	if err := compatibleType(typ, definition.ValueType); err != nil {
		return &Error{Kind: ErrDefinition, Code: code, Message: err.Error(), Cause: err}
	}
	if definition.AllowNull && !nullableType(typ) {
		return constantError(ErrDefinition, 0, code, "AllowNull requires a pointer, map, or slice Go type")
	}
	if len(definition.Editor) != 0 && !json.Valid(definition.Editor) {
		return constantError(ErrDefinition, 0, code, "editor metadata must be valid JSON")
	}
	registered := registeredDefinition{
		code: code, valueType: definition.ValueType, required: definition.Required,
		allowNull: definition.AllowNull, goType: typ,
		editor: append(json.RawMessage(nil), definition.Editor...),
		check: func(ctx context.Context, raw json.RawMessage) error {
			if err := validateRawType(definition.ValueType, definition.AllowNull, raw); err != nil {
				return err
			}
			value, err := decodeTyped[T](raw)
			if err != nil {
				return err
			}
			if definition.Validate != nil {
				return definition.Validate(ctx, value)
			}
			return nil
		},
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.definitions == nil {
		registry.definitions = make(map[string]registeredDefinition)
	}
	if _, exists := registry.definitions[code]; exists {
		return constantError(ErrDefinition, 0, code, "code is already registered")
	}
	registry.definitions[code] = registered
	return nil
}

func (r *Registry) snapshot() map[string]registeredDefinition {
	if r == nil {
		return map[string]registeredDefinition{}
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make(map[string]registeredDefinition, len(r.definitions))
	for code, definition := range r.definitions {
		definition.editor = append(json.RawMessage(nil), definition.editor...)
		result[code] = definition
	}
	return result
}

var (
	decimalPattern      = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?$`)
	jsonUnmarshalerType = reflect.TypeFor[json.Unmarshaler]()
)

// validateRawType checks the logical wire type without decoding generic JSON
// numbers into float64. Exact decimals are represented by JSON strings.
func validateRawType(valueType ValueType, allowNull bool, raw json.RawMessage) error {
	trimmed := bytes.TrimSpace(raw)
	if !knownValueType(valueType) {
		return fmt.Errorf("%w: unknown value type %q", ErrDefinition, valueType)
	}
	if !json.Valid(trimmed) {
		return fmt.Errorf("%w: value must contain exactly one valid JSON value", ErrValidation)
	}
	if bytes.Equal(trimmed, []byte("null")) {
		if allowNull {
			return nil
		}
		return fmt.Errorf("%w: null is not allowed", ErrValidation)
	}
	invalid := func(message string) error { return fmt.Errorf("%w: %s", ErrValidation, message) }
	switch valueType {
	case TypeBool:
		if !bytes.Equal(trimmed, []byte("true")) && !bytes.Equal(trimmed, []byte("false")) {
			return invalid("expected a JSON boolean")
		}
	case TypeInt64:
		if _, err := strconv.ParseInt(string(trimmed), 10, 64); err != nil {
			return invalid("expected a signed 64-bit JSON integer")
		}
	case TypeFloat64:
		value, err := strconv.ParseFloat(string(trimmed), 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
			return invalid("expected a finite float64 JSON number")
		}
	case TypeString, TypeDecimal:
		var value string
		if err := json.Unmarshal(trimmed, &value); err != nil {
			return invalid("expected a JSON string")
		}
		if valueType == TypeDecimal && !decimalPattern.MatchString(value) {
			return invalid("expected an exact decimal string, for example \"123.45\"")
		}
	case TypeJSON:
		// Concrete Go decoding and the project validator establish its schema.
	}
	return nil
}

func knownValueType(valueType ValueType) bool {
	switch valueType {
	case TypeBool, TypeInt64, TypeFloat64, TypeString, TypeDecimal, TypeJSON:
		return true
	default:
		return false
	}
}

func compatibleType(typ reflect.Type, valueType ValueType) error {
	if !knownValueType(valueType) {
		return fmt.Errorf("unknown value type %q", valueType)
	}
	if containsInterface(typ, make(map[reflect.Type]bool)) {
		return fmt.Errorf("Go type %s contains an interface; use a concrete type or json.RawMessage", typ)
	}
	if implementsJSONUnmarshaler(typ) {
		return nil
	}
	base := typ
	for base.Kind() == reflect.Pointer {
		base = base.Elem()
	}
	valid := false
	switch valueType {
	case TypeBool:
		valid = base.Kind() == reflect.Bool
	case TypeInt64:
		valid = base.Kind() >= reflect.Int && base.Kind() <= reflect.Int64
	case TypeFloat64:
		valid = base.Kind() == reflect.Float32 || base.Kind() == reflect.Float64
	case TypeString, TypeDecimal:
		valid = base.Kind() == reflect.String
	case TypeJSON:
		valid = true
	}
	if !valid {
		return fmt.Errorf("Go type %s is incompatible with logical value type %q", typ, valueType)
	}
	return nil
}

func nullableType(typ reflect.Type) bool {
	switch typ.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice:
		return true
	default:
		return false
	}
}

func implementsJSONUnmarshaler(typ reflect.Type) bool {
	if typ.Implements(jsonUnmarshalerType) {
		return true
	}
	return typ.Kind() != reflect.Pointer && reflect.PointerTo(typ).Implements(jsonUnmarshalerType)
}

func containsInterface(typ reflect.Type, visited map[reflect.Type]bool) bool {
	if typ.Kind() == reflect.Interface {
		return true
	}
	if visited[typ] || implementsJSONUnmarshaler(typ) {
		return false
	}
	visited[typ] = true
	switch typ.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array:
		return containsInterface(typ.Elem(), visited)
	case reflect.Map:
		return containsInterface(typ.Key(), visited) || containsInterface(typ.Elem(), visited)
	case reflect.Struct:
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			if field.IsExported() && field.Tag.Get("json") != "-" && containsInterface(field.Type, visited) {
				return true
			}
		}
	}
	return false
}

func decodeTyped[T any](raw json.RawMessage) (T, error) {
	var value T
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return value, err
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		if err != nil {
			return value, err
		}
		return value, fmt.Errorf("unexpected data after JSON value")
	}
	return value, nil
}
