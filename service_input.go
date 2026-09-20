package webapp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
)

// LookupService returns one registered service definition.
func LookupService(serviceName string) (ServiceDef, error) {
	serviceRegistryMu.RLock()
	defer serviceRegistryMu.RUnlock()

	serviceDef, ok := serviceRegistry[serviceName]
	if !ok {
		return ServiceDef{}, fmt.Errorf("service not registered: %s", serviceName)
	}

	return serviceDef, nil
}

// LookupMethod returns reflection metadata for one registered service method.
func LookupMethod(serviceName string, methodName string) (MethodMeta, error) {
	serviceDef, err := LookupService(serviceName)
	if err != nil {
		return MethodMeta{}, err
	}

	meta, ok := serviceDef.Methods[methodName]
	if !ok {
		return MethodMeta{}, fmt.Errorf("service method not registered: %s.%s", serviceName, methodName)
	}

	return meta, nil
}

// DecodeInputForService decodes a raw JSON payload into the exact input type
// required by a registered service method. It is intended for WebSocket and
// PostgreSQL LISTEN/NOTIFY calls where there is no http.Request binder.
func DecodeInputForService(serviceName string, methodName string, raw json.RawMessage) (any, error) {
	return DecodeInputForServiceContext(context.Background(), serviceName, methodName, raw)
}

func decodeInputForServiceLegacy(serviceName string, methodName string, raw json.RawMessage) (any, error) {
	meta, err := LookupMethod(serviceName, methodName)
	if err != nil {
		return nil, err
	}

	if !meta.HasInput {
		if isEmptyJSON(raw) {
			return nil, nil
		}

		return nil, fmt.Errorf("method %s.%s does not accept input", serviceName, methodName)
	}

	if isEmptyJSON(raw) {
		return nil, fmt.Errorf("method %s.%s requires input", serviceName, methodName)
	}

	inputType := meta.InputType
	if inputType.Kind() == reflect.Pointer {
		value := reflect.New(inputType.Elem())
		if err := json.Unmarshal(raw, value.Interface()); err != nil {
			return nil, err
		}

		return value.Interface(), nil
	}

	value := reflect.New(inputType)
	if err := json.Unmarshal(raw, value.Interface()); err != nil {
		return nil, err
	}

	return value.Elem().Interface(), nil
}

func isEmptyJSON(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null"))
}
