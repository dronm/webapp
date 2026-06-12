package webapp

import (
	"context"
	"fmt"
	"reflect"
	"sync"

	"github.com/dronm/ds/v4"
	"github.com/dronm/session"
)

// ServiceFactory creates a fresh service instance for one request or service call.
type ServiceFactory func(ctx ServiceContext) any

// ServiceContext contains request-scoped dependencies passed to service factories.
type ServiceContext struct {
	DB      ds.Provider
	Session session.Session
	QueryID string
}

// ServiceDef describes one registered service and its callable methods.
type ServiceDef struct {
	Factory ServiceFactory
	Methods map[string]MethodMeta
}

var (
	serviceRegistryMu sync.RWMutex
	serviceRegistry   = map[string]ServiceDef{}
)

// MethodMeta contains reflection metadata for one callable service method.
//
// Supported method signatures are:
//
//	func (s *Service) Method(ctx context.Context) error
//	func (s *Service) Method(ctx context.Context, input Input) error
//	func (s *Service) Method(ctx context.Context) (Result, error)
//	func (s *Service) Method(ctx context.Context, input Input) (Result, error)
type MethodMeta struct {
	Method       reflect.Method
	ReceiverType reflect.Type
	InputType    reflect.Type
	HasInput     bool
	ResultType   reflect.Type
	HasResult    bool
}

// RegisterService registers exported service methods that match the supported signatures.
func RegisterService(name string, prototype any, factory ServiceFactory) error {
	if name == "" {
		return fmt.Errorf("service name is empty")
	}

	if prototype == nil {
		return fmt.Errorf("service prototype is nil")
	}

	if factory == nil {
		return fmt.Errorf("service factory is nil")
	}

	t := reflect.TypeOf(prototype)

	contextType := reflect.TypeFor[context.Context]()
	errorType := reflect.TypeFor[error]()

	methods := map[string]MethodMeta{}

	for i := 0; i < t.NumMethod(); i++ {
		m := t.Method(i)
		if !m.IsExported() {
			continue
		}

		mt := m.Type

		// receiver + context + optional input
		if mt.NumIn() != 2 && mt.NumIn() != 3 {
			continue
		}

		if mt.In(1) != contextType {
			continue
		}

		// Require either error or (result, error).
		if mt.NumOut() != 1 && mt.NumOut() != 2 {
			continue
		}

		if mt.Out(mt.NumOut()-1) != errorType {
			continue
		}

		meta := MethodMeta{
			Method:       m,
			ReceiverType: mt.In(0),
		}

		if mt.NumIn() == 3 {
			meta.HasInput = true
			meta.InputType = mt.In(2)
		}

		if mt.NumOut() == 2 {
			meta.HasResult = true
			meta.ResultType = mt.Out(0)
		}

		methods[m.Name] = meta
	}

	if len(methods) == 0 {
		return fmt.Errorf("service %s has no callable methods", name)
	}

	serviceRegistryMu.Lock()
	defer serviceRegistryMu.Unlock()

	serviceRegistry[name] = ServiceDef{
		Factory: factory,
		Methods: methods,
	}

	return nil
}

// MustRegisterService is a startup helper that panics if registration fails.
func MustRegisterService(name string, prototype any, factory ServiceFactory) {
	if err := RegisterService(name, prototype, factory); err != nil {
		panic(err)
	}
}

// CallServiceMethod creates a service instance, calls a registered method, and returns its result.
func CallServiceMethod(
	ctx context.Context,
	serviceName string,
	methodName string,
	input any,
	svcCtx ServiceContext,
) (any, error) {
	serviceRegistryMu.RLock()
	serviceDef, ok := serviceRegistry[serviceName]
	serviceRegistryMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("service not registered: %s", serviceName)
	}

	meta, ok := serviceDef.Methods[methodName]
	if !ok {
		return nil, fmt.Errorf("service method not registered: %s.%s", serviceName, methodName)
	}

	service := serviceDef.Factory(svcCtx)
	if service == nil {
		return nil, fmt.Errorf("service factory returned nil: %s", serviceName)
	}

	receiver := reflect.ValueOf(service)
	if !receiver.Type().AssignableTo(meta.ReceiverType) {
		return nil, fmt.Errorf(
			"invalid receiver type for %s.%s: expected %s, got %s",
			serviceName,
			methodName,
			meta.ReceiverType,
			receiver.Type(),
		)
	}

	args := []reflect.Value{
		reflect.ValueOf(ctx),
	}

	if meta.HasInput {
		if input == nil {
			return nil, fmt.Errorf("method %s.%s requires input", serviceName, methodName)
		}

		inputValue := reflect.ValueOf(input)

		if inputValue.Type().AssignableTo(meta.InputType) {
			args = append(args, inputValue)
		} else if inputValue.Type().ConvertibleTo(meta.InputType) {
			args = append(args, inputValue.Convert(meta.InputType))
		} else {
			return nil, fmt.Errorf(
				"invalid input type for %s.%s: expected %s, got %s",
				serviceName,
				methodName,
				meta.InputType,
				inputValue.Type(),
			)
		}
	} else if input != nil {
		return nil, fmt.Errorf("method %s.%s does not accept input", serviceName, methodName)
	}

	out := meta.Method.Func.Call(append([]reflect.Value{receiver}, args...))

	errValue := out[len(out)-1]
	if !errValue.IsNil() {
		return nil, errValue.Interface().(error)
	}

	if !meta.HasResult {
		return nil, nil
	}

	return out[0].Interface(), nil
}
