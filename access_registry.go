package webapp

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"

	"github.com/dronm/modelbind/access"
	modelpg "github.com/dronm/modelbind/pg"
	"github.com/dronm/modelbind/types"
)

// PolicyFactory returns one resolved restriction for the registered operation.
// Factories receive trusted principal data. Use ordinary trusted lookup SQL for
// identity/assignment resolution; do not recursively resolve the same policy.
type PolicyFactory func(context.Context, Principal) (access.Policy, error)

type policyRegistration struct {
	role    string
	user    string
	factory PolicyFactory
}

type modelAccessBinding struct {
	resource string
	relation string
	binding  modelpg.PolicyBinding
}

// PolicyRegistry composes application, role and user presets restrictively.
// All matching row predicates are ANDed. No match is denied. DenyAll wins.
// Duplicate identical write rules are allowed; conflicting rules on one field
// are configuration errors (never last-writer-wins). Resolve overlapping write
// constraints explicitly in a single factory when an intersection is required.
type PolicyRegistry struct {
	mu        sync.RWMutex
	models    map[reflect.Type]modelAccessBinding
	relations map[string]string
	policies  map[policyCacheKey][]policyRegistration
}

func NewPolicyRegistry() *PolicyRegistry {
	return &PolicyRegistry{}
}

// RegisterModel binds one concrete model representation. Models MUST implement
// types.AccessScopedModel so loss of the request context cannot silently disable
// protection. Register write, list, detail and delete-key models independently.
// Either a value or a pointer prototype is accepted; a value prototype must
// itself implement the marker. Bindings are copied at registration time.
func (r *PolicyRegistry) RegisterModel(model types.DBModel, binding modelpg.PolicyBinding) error {
	if r == nil || nilInterface(model) {
		return fmt.Errorf("%w: registry/model is nil", ErrAccessConfiguration)
	}
	marker, ok := model.(types.AccessScopedModel)
	if !ok || strings.TrimSpace(marker.AccessResource()) == "" {
		return fmt.Errorf("%w: %T must implement a nonempty AccessResource", ErrAccessConfiguration, model)
	}
	resource := strings.TrimSpace(marker.AccessResource())
	if strings.TrimSpace(model.Relation()) == "" {
		return fmt.Errorf("%w: model relation is empty", ErrAccessConfiguration)
	}
	// Validate even currently unused mappings at startup.
	checks := make([]access.Predicate, 0, len(binding.Rows))
	for field := range binding.Rows {
		checks = append(checks, access.IsNotNull(field))
	}
	if len(checks) > 0 {
		params := []any{}
		if _, err := modelpg.CompilePredicate(access.And(checks...), binding.Rows, &params); err != nil {
			return fmt.Errorf("%w: %w", ErrAccessConfiguration, err)
		}
	}
	if len(binding.Writes) > 0 {
		rules := make(access.WriteRules, len(binding.Writes))
		for field := range binding.Writes {
			rules[field] = access.Enforce(nil)
		}
		builder := modelpg.NewPgInsert(model)
		if err := builder.SetAccessPolicy(access.ForInsert(rules), binding); err != nil {
			return fmt.Errorf("%w: %w", ErrAccessConfiguration, err)
		}
	}
	typ := accessModelType(model)
	if typ.Implements(reflect.TypeFor[types.DBModel]()) && !typ.Implements(reflect.TypeFor[types.AccessScopedModel]()) {
		return fmt.Errorf("%w: define AccessResource with a value receiver when Relation has a value receiver", ErrAccessConfiguration)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.models == nil {
		r.models = make(map[reflect.Type]modelAccessBinding)
		r.relations = make(map[string]string)
	}
	if _, exists := r.models[typ]; exists {
		return fmt.Errorf("%w: model %s is already registered", ErrAccessConfiguration, typ)
	}
	if previous, exists := r.relations[model.Relation()]; exists && previous != resource {
		return fmt.Errorf("%w: relation is already bound to another resource", ErrAccessConfiguration)
	}
	r.models[typ] = modelAccessBinding{resource, model.Relation(), clonePolicyBinding(binding)}
	r.relations[model.Relation()] = resource
	return nil
}

func (r *PolicyRegistry) Register(resource string, operation access.Operation, factory PolicyFactory) error {
	return r.register(resource, operation, policyRegistration{factory: factory})
}

func (r *PolicyRegistry) RegisterRole(role, resource string, operation access.Operation, factory PolicyFactory) error {
	if strings.TrimSpace(role) == "" {
		return fmt.Errorf("%w: role is empty", ErrAccessConfiguration)
	}
	return r.register(resource, operation, policyRegistration{role: strings.TrimSpace(role), factory: factory})
}

func (r *PolicyRegistry) RegisterUser(userID, resource string, operation access.Operation, factory PolicyFactory) error {
	if strings.TrimSpace(userID) == "" {
		return fmt.Errorf("%w: user ID is empty", ErrAccessConfiguration)
	}
	return r.register(resource, operation, policyRegistration{user: strings.TrimSpace(userID), factory: factory})
}

func (r *PolicyRegistry) register(resource string, operation access.Operation, registration policyRegistration) error {
	if r == nil || strings.TrimSpace(resource) == "" || registration.factory == nil {
		return fmt.Errorf("%w: registry/resource/factory is missing", ErrAccessConfiguration)
	}
	if err := access.AllowAll().Validate(operation); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.policies == nil {
		r.policies = make(map[policyCacheKey][]policyRegistration)
	}
	key := policyCacheKey{strings.TrimSpace(resource), operation}
	r.policies[key] = append(r.policies[key], registration)
	return nil
}

func (r *PolicyRegistry) modelBinding(model types.DBModel, resource string) (modelpg.PolicyBinding, error) {
	r.mu.RLock()
	registration, ok := r.models[accessModelType(model)]
	r.mu.RUnlock()
	if !ok || registration.resource != resource || registration.relation != model.Relation() {
		return modelpg.PolicyBinding{}, fmt.Errorf("%w: model %T needs an explicit matching binding for %s", ErrAccessConfiguration, model, resource)
	}
	return clonePolicyBinding(registration.binding), nil
}

func (r *PolicyRegistry) protectsRelation(relation string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.relations[relation]
	return ok
}

func (r *PolicyRegistry) resolve(ctx context.Context, principal Principal, resource string, operation access.Operation) (access.Policy, error) {
	if err := ctx.Err(); err != nil {
		return access.Policy{}, err
	}
	r.mu.RLock()
	registrations := append([]policyRegistration(nil), r.policies[policyCacheKey{resource, operation}]...)
	r.mu.RUnlock()
	if len(registrations) == 0 {
		return access.Policy{}, fmt.Errorf("%w: no %s policy configured for %s", ErrAccessConfiguration, operation, resource)
	}
	roles := make(map[string]bool, len(principal.Roles))
	for _, role := range principal.Roles {
		roles[role] = true
	}
	matched := make([]access.Policy, 0, len(registrations))
	for _, registration := range registrations {
		if registration.role != "" && !roles[registration.role] || registration.user != "" && registration.user != principal.UserID {
			continue
		}
		copyPrincipal, err := snapshotPrincipal(principal)
		if err != nil {
			return access.Policy{}, err
		}
		policy, err := registration.factory(ctx, copyPrincipal)
		if err != nil {
			return access.Policy{}, fmt.Errorf("%w: %w", ErrPolicyResolution, err)
		}
		matched = append(matched, policy)
	}
	return IntersectAccessPolicies(operation, matched...)
}

// IntersectAccessPolicies exposes the same restrictive composition for custom
// resolvers. Empty input denies. AllowAll is an identity, never an admin override.
func IntersectAccessPolicies(operation access.Operation, policies ...access.Policy) (access.Policy, error) {
	if err := access.AllowAll().Validate(operation); err != nil {
		return access.Policy{}, err
	}
	if len(policies) == 0 {
		return access.Policy{}, access.ErrDenied
	}
	rows := make([]access.Predicate, 0, len(policies))
	writes := make(access.WriteRules)
	denied := false
	for _, policy := range policies {
		if err := policy.Validate(operation); err != nil {
			return access.Policy{}, err
		}
		if policy.Decision == access.Denied {
			denied = true
		}
		if policy.Decision != access.Restricted {
			continue
		}
		if !policy.Rows.IsZero() {
			rows = append(rows, policy.Rows)
		}
		for _, field := range policy.Writes.Fields() {
			rule := policy.Writes[field]
			if previous, exists := writes[field]; exists && !reflect.DeepEqual(previous, rule) {
				return access.Policy{}, fmt.Errorf("%w: overlapping write rules for %q require explicit composition", ErrAccessConfiguration, field)
			}
			writes[field] = rule
		}
	}
	if denied {
		return access.Policy{}, access.ErrDenied
	}
	if len(rows) == 0 && len(writes) == 0 {
		return access.AllowAll(), nil
	}
	result := access.Policy{Decision: access.Restricted, Writes: writes}
	if len(rows) == 1 {
		result.Rows = rows[0]
	} else if len(rows) > 1 {
		result.Rows = access.And(rows...)
	}
	return result, result.Validate(operation)
}

func accessModelType(model any) reflect.Type {
	typ := reflect.TypeOf(model)
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	return typ
}

func clonePolicyBinding(binding modelpg.PolicyBinding) modelpg.PolicyBinding {
	clone := func(fields modelpg.FieldMap) modelpg.FieldMap {
		copy := make(modelpg.FieldMap, len(fields))
		keys := make([]string, 0, len(fields))
		for key := range fields {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			copy[key] = fields[key]
		}
		return copy
	}
	return modelpg.PolicyBinding{Rows: clone(binding.Rows), Writes: clone(binding.Writes)}
}
