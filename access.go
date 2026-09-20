package webapp

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"

	"github.com/dronm/modelbind/access"
	modelpg "github.com/dronm/modelbind/pg"
	"github.com/dronm/modelbind/types"
	"github.com/dronm/session"
)

var (
	ErrPrincipalRequired   = errors.New("access principal is required")
	ErrAccessConfiguration = errors.New("access policy configuration is invalid")
	ErrPolicyResolution    = errors.New("access policy resolution failed")
)

// Principal contains trusted, application-resolved identity. Claims are scalar
// values supported by access.NormalizeValue. Load relationship sets (for example
// assigned sites) in a PolicyFactory, not from request input or session snapshots.
// UserID is an application identifier, not a session ID. System jobs must also
// have an explicit identity and an applicable policy; there is no bypass flag.
type Principal struct {
	UserID string
	Roles  []string
	Claims map[string]any
}

// PrincipalResolver is called lazily, once per access context. An anonymous
// session may return Principal{}, nil. A protected operation will then fail.
// Recheck login/session validity here, especially for long-lived WebSockets.
type PrincipalResolver func(context.Context, session.Session) (Principal, error)

// AccessController is application-wide configuration. Configure it at startup;
// do not mutate its fields while serving requests. Registry registrations are
// concurrency-safe. Each HTTP request / WebSocket message gets its own cache.
type AccessController struct {
	Registry         *PolicyRegistry
	ResolvePrincipal PrincipalResolver
}

// ResolvedAccess is a snapshot for one concrete model and operation. Applied is
// false only for a legacy, unmarked model. Explicit AllowAll still has Applied
// true and retains protected-path safeguards.
type ResolvedAccess struct {
	Applied   bool
	Resource  string
	Operation access.Operation
	Policy    access.Policy
	Binding   modelpg.PolicyBinding
}

// AccessPolicySetter is implemented by modelbind's PostgreSQL CRUD builders.
type AccessPolicySetter interface {
	SetAccessPolicy(access.Policy, modelpg.PolicyBinding) error
}

// ApplyTo installs the policy, including an explicit unrestricted decision.
func (r ResolvedAccess) ApplyTo(builder AccessPolicySetter) error {
	if !r.Applied {
		return nil
	}
	if nilInterface(builder) {
		return NormalizeAccessError(fmt.Errorf("%w: nil policy builder", ErrAccessConfiguration))
	}
	var operation access.Operation
	switch builder.(type) {
	case *modelpg.PgSelect, *modelpg.PgDetailSelect:
		operation = access.Read
	case *modelpg.PgInsert:
		operation = access.Insert
	case *modelpg.PgUpdate:
		operation = access.Update
	case *modelpg.PgDelete:
		operation = access.Delete
	}
	if operation == "" || operation != r.Operation {
		// Poison the builder too: ignoring this error must not turn a failed
		// installation into an unrestricted query.
		_ = builder.SetAccessPolicy(access.DenyAll(), r.Binding)
		return NormalizeAccessError(fmt.Errorf("%w: resolved policy/builder operation mismatch", ErrAccessConfiguration))
	}
	return NormalizeAccessError(builder.SetAccessPolicy(r.Policy, r.Binding))
}

const (
	accessContextKey    contextKey = "webapp.access"
	principalContextKey contextKey = "webapp.principal"
)

type principalValue struct {
	principal Principal
	err       error
}

type policyCacheKey struct {
	resource  string
	operation access.Operation
}

type policyCacheEntry struct {
	once   sync.Once
	policy access.Policy
	err    error
}

type accessState struct {
	controller    *AccessController
	principalOnce sync.Once
	principal     Principal
	principalErr  error
	mu            sync.Mutex
	policies      map[policyCacheKey]*policyCacheEntry
	used          bool
}

// ContextWithAccess starts a NEW authorization scope, including a new identity
// and policy cache. Use once per job, request or WebSocket message, not per SQL
// statement. Do not reuse it between users or indefinitely across operations.
func ContextWithAccess(ctx context.Context, controller *AccessController) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, accessContextKey, &accessState{
		controller: controller,
		policies:   make(map[policyCacheKey]*policyCacheEntry),
	})
}

// ContextWithPrincipal supplies an already authenticated identity from TRUSTED
// server code. It does not grant access by itself. It resets any existing cache.
// Never build this value directly from JSON, query parameters or headers.
func ContextWithPrincipal(ctx context.Context, principal Principal) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	p, err := snapshotPrincipal(principal)
	ctx = context.WithValue(ctx, principalContextKey, principalValue{p, err})
	if state := accessStateFromContext(ctx); state != nil {
		ctx = ContextWithAccess(ctx, state.controller)
	}
	return ctx
}

func accessStateFromContext(ctx context.Context) *accessState {
	if ctx == nil {
		return nil
	}
	state, _ := ctx.Value(accessContextKey).(*accessState)
	return state
}

func snapshotPrincipal(principal Principal) (Principal, error) {
	result := Principal{UserID: strings.TrimSpace(principal.UserID)}
	seen := make(map[string]bool)
	for _, role := range principal.Roles {
		role = strings.TrimSpace(role)
		if role != "" && !seen[role] {
			result.Roles = append(result.Roles, role)
			seen[role] = true
		}
	}
	result.Claims = make(map[string]any, len(principal.Claims))
	for key, value := range principal.Claims {
		normalized, err := access.NormalizeValue(value)
		if err != nil {
			return Principal{}, fmt.Errorf("%w: unsupported principal claim %q: %w", ErrAccessConfiguration, key, err)
		}
		result.Claims[key] = normalized
	}
	return result, nil
}

func (s *accessState) resolvePrincipal(ctx context.Context) (Principal, error) {
	s.principalOnce.Do(func() {
		if supplied, ok := ctx.Value(principalContextKey).(principalValue); ok {
			s.principal, s.principalErr = supplied.principal, supplied.err
		} else if s.controller != nil && s.controller.ResolvePrincipal != nil {
			var principal Principal
			principal, s.principalErr = s.controller.ResolvePrincipal(ctx, SessionFromContext(ctx))
			if s.principalErr != nil {
				s.principalErr = fmt.Errorf("%w: %w", ErrPolicyResolution, s.principalErr)
			} else {
				s.principal, s.principalErr = snapshotPrincipal(principal)
			}
		}
		if s.principalErr == nil && s.principal.UserID == "" {
			s.principalErr = ErrPrincipalRequired
		}
	})
	if s.principalErr != nil {
		return Principal{}, s.principalErr
	}
	return snapshotPrincipal(s.principal)
}

// PrincipalFromContext returns the authenticated principal for this access
// scope. The returned scalar claims and role slice are independent copies.
func PrincipalFromContext(ctx context.Context) (Principal, error) {
	state := accessStateFromContext(ctx)
	if state == nil {
		return Principal{}, NormalizeAccessError(ErrPrincipalRequired)
	}
	principal, err := state.resolvePrincipal(ctx)
	return principal, NormalizeAccessError(err)
}

func (s *accessState) resolvePolicy(ctx context.Context, resource string, operation access.Operation) (access.Policy, error) {
	key := policyCacheKey{resource, operation}
	s.mu.Lock()
	s.used = true
	entry := s.policies[key]
	if entry == nil {
		entry = &policyCacheEntry{}
		s.policies[key] = entry
	}
	s.mu.Unlock()
	entry.once.Do(func() {
		principal, err := s.resolvePrincipal(ctx)
		if err != nil {
			entry.err = err
			return
		}
		entry.policy, entry.err = s.controller.Registry.resolve(ctx, principal, resource, operation)
	})
	return entry.policy.Clone(), entry.err
}

// ResolveModelAccess resolves a marked model's policy and its explicit SQL
// binding. No Relation SQL parsing or client filters are used to infer policies.
// A marker without configuration ALWAYS fails, even with context.Background().
func ResolveModelAccess(ctx context.Context, model any, operation access.Operation) (ResolvedAccess, error) {
	resolved, err := resolveModelAccess(ctx, model, operation)
	return resolved, NormalizeAccessError(err)
}

func resolveModelAccess(ctx context.Context, model any, operation access.Operation) (ResolvedAccess, error) {
	if nilInterface(model) {
		return ResolvedAccess{}, fmt.Errorf("%w: nil model", ErrAccessConfiguration)
	}
	marker, marked := model.(types.AccessScopedModel)
	state := accessStateFromContext(ctx)
	if !marked {
		// Detect accidentally omitted markers on another representation of an
		// already protected relation when the application scope is available.
		if dbModel, ok := model.(types.DBModel); ok && state != nil && state.controller != nil && state.controller.Registry != nil {
			if state.controller.Registry.protectsRelation(dbModel.Relation()) {
				return ResolvedAccess{}, fmt.Errorf("%w: unmarked model %T uses a protected relation", ErrAccessConfiguration, model)
			}
		}
		return ResolvedAccess{}, nil
	}
	resource := strings.TrimSpace(marker.AccessResource())
	if resource == "" {
		return ResolvedAccess{}, fmt.Errorf("%w: empty resource on %T", ErrAccessConfiguration, model)
	}
	if state == nil || state.controller == nil || state.controller.Registry == nil {
		return ResolvedAccess{}, fmt.Errorf("%w: controller missing for %s", ErrAccessConfiguration, resource)
	}
	dbModel, ok := model.(types.DBModel)
	if !ok {
		return ResolvedAccess{}, fmt.Errorf("%w: scoped model %T is not a DBModel", ErrAccessConfiguration, model)
	}
	binding, err := state.controller.Registry.modelBinding(dbModel, resource)
	if err != nil {
		return ResolvedAccess{}, err
	}
	policy, err := state.resolvePolicy(ctx, resource, operation)
	if err != nil {
		return ResolvedAccess{}, err
	}
	return ResolvedAccess{true, resource, operation, policy, binding}, nil
}

func nilInterface(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice, reflect.Func:
		return v.IsNil()
	}
	return false
}

// PrepareServiceContext installs session, query ID and authorization state before
// binding non-HTTP inputs. Calling it again with the same controller preserves
// the current request snapshot. Use ContextWithAccess to start a NEW call scope.
func PrepareServiceContext(ctx context.Context, svcCtx ServiceContext) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if svcCtx.Session != nil && !sameAccessSession(SessionFromContext(ctx), svcCtx.Session) {
		ctx = ContextWithSession(ctx, svcCtx.Session)
	}
	state := accessStateFromContext(ctx)
	if svcCtx.QueryID != "" {
		ctx = ContextWithQueryID(ctx, svcCtx.QueryID)
	}
	if svcCtx.Access != nil && (state == nil || state.controller != svcCtx.Access) {
		ctx = ContextWithAccess(ctx, svcCtx.Access)
	}
	return ctx
}

// Session implementations are normally pointers. A non-comparable
// implementation cannot safely share an authorization snapshot.
func sameAccessSession(a, b session.Session) bool {
	if nilInterface(a) || nilInterface(b) {
		return nilInterface(a) && nilInterface(b)
	}
	av, bv := reflect.ValueOf(a), reflect.ValueOf(b)
	return av.Type() == bv.Type() && av.Comparable() && av.Interface() == bv.Interface()
}
