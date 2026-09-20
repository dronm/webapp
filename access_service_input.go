package webapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/dronm/modelbind"
	"github.com/dronm/modelbind/types"
)

// ServiceInputDecoder decodes a transport payload before service invocation.
// Unlike a plain json.Unmarshal into ModelInput, it can preserve field absence.
type ServiceInputDecoder func(context.Context, json.RawMessage) (any, error)

// WithMethodInputDecoder configures WebSocket / local-event JSON decoding for a
// method. It does not replace an HTTP route's Binder. Startup registration checks
// that the method exists and takes input; invocation checks its exact input type.
func WithMethodInputDecoder(method string, decoder ServiceInputDecoder) ServiceOption {
	return func(service *ServiceDef) {
		if service.InputDecoders == nil {
			service.InputDecoders = make(map[string]ServiceInputDecoder)
		}
		service.InputDecoders[method] = decoder
	}
}

// ModelInputJSONDecoder accepts a FLAT model object, not a serialized ModelInput
// envelope. The browser cannot supply AbsentFields. Modelbind performs typed
// decoding; server write values are applied before required-field validation.
func ModelInputJSONDecoder[T any](forInsert bool) ServiceInputDecoder {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		if err := requireJSONObject(raw); err != nil {
			return nil, err
		}
		input, err := modelbind.DecodeJSONBodyInput[T](bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		return prepareBoundInput(ctx, input, forInsert)
	}
}

// ModelJSONDecoder is the full-model counterpart. PATCH handlers should prefer
// ModelInputJSONDecoder; discarding field presence means all fields are supplied
// when later calling a full-model CRUD helper.
func ModelJSONDecoder[T any](forInsert bool) ServiceInputDecoder {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		decoded, err := ModelInputJSONDecoder[T](forInsert)(ctx, raw)
		if err != nil {
			return nil, err
		}
		return decoded.(modelbind.ModelInput[T]).Model, nil
	}
}

// UpdateByKeysJSONDecoder accepts {"keys":{...},"input":{...}}. Both objects
// are decoded with modelbind; Input remains a flat model object on the wire.
func UpdateByKeysJSONDecoder[K types.DBModel, M types.DBModel]() ServiceInputDecoder {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		if err := requireJSONObject(raw); err != nil {
			return nil, err
		}
		var envelope struct {
			Keys  json.RawMessage `json:"keys"`
			Input json.RawMessage `json:"input"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return nil, BadRequest("Invalid update payload", nil)
		}
		if err := requireJSONObject(envelope.Keys); err != nil {
			return nil, BadRequest("A keys object is required", nil)
		}
		keys, err := modelbind.DecodeJSONBodyInput[K](bytes.NewReader(envelope.Keys))
		if err != nil {
			return nil, err
		}
		// Unlike plain struct decoding, detect absent numeric keys before they
		// collapse to their zero values. No partial composite keys are allowed.
		if keys.AbsentFields.Len() != 0 {
			return nil, NormalizeAccessError(modelbind.ErrMissingKey)
		}
		if err := modelbind.RequireModelKeys(keys.Model); err != nil {
			return nil, NormalizeAccessError(err)
		}
		if err := keys.Validate(true); err != nil {
			return nil, err
		}
		decoded, err := ModelInputJSONDecoder[M](false)(ctx, envelope.Input)
		if err != nil {
			return nil, err
		}
		return UpdateByKeysInput[K, M]{Keys: keys.Model, Input: decoded.(modelbind.ModelInput[M])}, nil
	}
}

// DecodeInputForServiceContext is the context-aware variant used by transports.
// A protected ModelInput requires a registered typed decoder rather than the
// legacy reflection fallback, which cannot recover original field presence.
func DecodeInputForServiceContext(ctx context.Context, serviceName, methodName string, raw json.RawMessage) (any, error) {
	service, err := LookupService(serviceName)
	if err != nil {
		return nil, err
	}
	meta, ok := service.Methods[methodName]
	if !ok {
		return nil, fmt.Errorf("service method not registered: %s.%s", serviceName, methodName)
	}
	if decoder := service.InputDecoders[methodName]; decoder != nil {
		input, err := decoder(ctx, raw)
		if err != nil {
			return nil, NormalizeAccessError(err)
		}
		if input == nil || !reflect.TypeOf(input).AssignableTo(meta.InputType) {
			return nil, NormalizeAccessError(fmt.Errorf("%w: decoder for %s.%s returned %T, expected %s", ErrAccessConfiguration, serviceName, methodName, input, meta.InputType))
		}
		return input, nil
	}
	if meta.HasInput && containsProtectedModelInput(meta.InputType, make(map[reflect.Type]bool)) {
		return nil, NormalizeAccessError(fmt.Errorf("%w: register a typed JSON decoder for %s.%s", ErrAccessConfiguration, serviceName, methodName))
	}
	return decodeInputForServiceLegacy(serviceName, methodName, raw)
}

// CallServiceJSON is the common WebSocket/local-event dispatch path. The caller
// should start a fresh access scope per message. Method-level permission checks
// remain the transport/application's separate responsibility.
func CallServiceJSON(ctx context.Context, serviceName, methodName string, raw json.RawMessage, svcCtx ServiceContext) (any, error) {
	ctx = PrepareServiceContext(ctx, svcCtx)
	input, err := DecodeInputForServiceContext(ctx, serviceName, methodName, raw)
	if err != nil {
		var httpErr HTTPError
		if errors.As(err, &httpErr) || IsModelBindError(err) {
			return nil, err
		}
		return nil, WrapHTTPError(400, "bad_request", "Invalid service input", nil, err)
	}
	return CallServiceMethod(ctx, serviceName, methodName, input, svcCtx)
}

func requireJSONObject(raw json.RawMessage) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' || !json.Valid(trimmed) {
		return BadRequest("A JSON object is required", nil)
	}
	return nil
}

func containsProtectedModelInput(typ reflect.Type, visited map[reflect.Type]bool) bool {
	if typ == nil || visited[typ] {
		return false
	}
	visited[typ] = true
	for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice || typ.Kind() == reflect.Array {
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Struct {
		return false
	}
	if typ.PkgPath() == reflect.TypeFor[modelbind.ModelInput[any]]().PkgPath() {
		modelField, hasModel := typ.FieldByName("Model")
		absenceField, hasAbsence := typ.FieldByName("AbsentFields")
		if hasModel && hasAbsence && absenceField.Type == reflect.TypeFor[modelbind.AbsentFieldSet]() {
			marker := reflect.TypeFor[types.AccessScopedModel]()
			return modelField.Type.Implements(marker) || reflect.PointerTo(modelField.Type).Implements(marker)
		}
	}
	for i := 0; i < typ.NumField(); i++ {
		if typ.Field(i).IsExported() && containsProtectedModelInput(typ.Field(i).Type, visited) {
			return true
		}
	}
	return false
}
