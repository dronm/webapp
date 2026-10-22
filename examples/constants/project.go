// Package projectconstants demonstrates integrating the common constants
// catalogue into a project that already uses webapp and ds/v4.
package projectconstants

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/dronm/ds/v4"
	"github.com/dronm/webapp"
	"github.com/dronm/webapp/constants"
)

const (
	CodeAllowOrderCreation  = "allow_order_creation"
	CodeOrderTimeoutSeconds = "order_timeout_seconds"
	CodeDefaultPriceType    = "default_price_type"
)

// DefaultPriceType is a project-owned value. The common library does not import
// this type or know the application's price-type catalogue.
type DefaultPriceType struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// Project owns one manager per process. Request services must share this manager.
type Project struct {
	Manager             *constants.Manager
	AllowOrderCreation  *constants.Constant[bool]
	OrderTimeoutSeconds *constants.Constant[int64]
	DefaultPriceType    *DefaultPriceTypeService
}

// Open expects the common migration and seed.sql to have been applied by the
// project's migration runner. db and primaryDSN must refer to the same primary
// database. The caller keeps ownership of db.
func Open(ctx context.Context, db ds.Provider, primaryDSN string) (*Project, error) {
	registry := constants.NewRegistry()
	if err := constants.Register(registry, constants.Definition[bool]{
		Code: CodeAllowOrderCreation, ValueType: constants.TypeBool, Required: true,
	}); err != nil {
		return nil, err
	}
	if err := constants.Register(registry, constants.Definition[int64]{
		Code: CodeOrderTimeoutSeconds, ValueType: constants.TypeInt64, Required: true,
		Editor: json.RawMessage(`{"minimum":1,"maximum":3600,"unit":"seconds"}`),
		Validate: func(_ context.Context, value int64) error {
			if value < 1 || value > 3600 {
				return errors.New("timeout must be between 1 and 3600 seconds")
			}
			return nil
		},
	}); err != nil {
		return nil, err
	}
	if err := constants.Register(registry, constants.Definition[DefaultPriceType]{
		Code: CodeDefaultPriceType, ValueType: constants.TypeJSON, Required: true,
		Validate: func(_ context.Context, value DefaultPriceType) error {
			if value.ID <= 0 || strings.TrimSpace(value.Name) == "" {
				return errors.New("a positive price type ID and a name are required")
			}
			// A real project can additionally verify value.ID against its own
			// price-type repository. Validators should not call this manager.
			return nil
		},
	}); err != nil {
		return nil, err
	}

	repository, err := constants.NewPostgresRepository(db)
	if err != nil {
		return nil, err
	}
	listener, err := constants.NewPGListener(primaryDSN)
	if err != nil {
		return nil, err
	}
	manager, err := constants.NewManager(constants.Config{
		Repository: repository, Registry: registry, Notifications: listener,
		ReconcileInterval: time.Minute,
		OnError: func(err error) {
			slog.Error("constants synchronization", "error", err)
		},
	})
	if err != nil {
		return nil, err
	}
	if err := manager.Start(ctx); err != nil {
		return nil, fmt.Errorf("start constants: %w", err)
	}
	success := false
	defer func() {
		if !success {
			closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = manager.Close(closeCtx)
		}
	}()

	allowOrders, err := constants.NewConstant[bool](manager, CodeAllowOrderCreation)
	if err != nil {
		return nil, err
	}
	timeout, err := constants.NewConstant[int64](manager, CodeOrderTimeoutSeconds)
	if err != nil {
		return nil, err
	}
	defaultPrice, err := NewConstantDefaultPriceTypeService(manager)
	if err != nil {
		return nil, err
	}
	success = true
	return &Project{
		Manager: manager, AllowOrderCreation: allowOrders,
		OrderTimeoutSeconds: timeout, DefaultPriceType: defaultPrice,
	}, nil
}

// RegisterRoutes captures the same manager for every request. Configure the
// application's authenticated PrincipalResolver before mounting these routes.
func (p *Project) RegisterRoutes(serviceName string) ([]webapp.Route, error) {
	if err := constants.RegisterService(serviceName, p.Manager, AuthorizeAdmin); err != nil {
		return nil, err
	}
	return constants.Routes("/constants", serviceName)
}

// AuthorizeAdmin runs for both HTTP and WebSocket service calls. A project can
// use operation and ids for finer permissions; this example grants the whole
// constants catalogue only to administrators.
func AuthorizeAdmin(ctx context.Context, _ constants.Operation, _ []int64) error {
	principal, err := webapp.PrincipalFromContext(ctx)
	if err != nil || principal.UserID == "" {
		return webapp.Unauthorized("Authentication is required", nil)
	}
	for _, role := range principal.Roles {
		if role == "admin" {
			return nil
		}
	}
	return webapp.Forbidden("Administrator access is required", nil)
}

func (p *Project) Close(ctx context.Context) error {
	return p.Manager.Close(ctx)
}

// DefaultPriceTypeService is an optional project convenience wrapper. Simple
// constants can use constants.Constant[T] directly instead.
type DefaultPriceTypeService struct {
	constant *constants.Constant[DefaultPriceType]
}

func NewConstantDefaultPriceTypeService(manager *constants.Manager) (*DefaultPriceTypeService, error) {
	constant, err := constants.NewConstant[DefaultPriceType](manager, CodeDefaultPriceType)
	if err != nil {
		return nil, err
	}
	return &DefaultPriceTypeService{constant: constant}, nil
}

func (s *DefaultPriceTypeService) GetValue(ctx context.Context) (DefaultPriceType, error) {
	return s.constant.GetValue(ctx)
}

func (s *DefaultPriceTypeService) SetValue(ctx context.Context, value DefaultPriceType) error {
	return s.constant.SetValue(ctx, value)
}
