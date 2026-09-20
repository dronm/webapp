package webapp

import (
	"context"
	"fmt"

	"github.com/dronm/modelbind"
	"github.com/dronm/modelbind/access"
	"github.com/dronm/modelbind/metadata"
	modelpg "github.com/dronm/modelbind/pg"
	"github.com/dronm/modelbind/types"
)

// PrepareAccessInput resolves the operation's policy, overlays trusted values
// and validates the effective input. The submitted model and presence metadata
// are unchanged. Standard write binders call this automatically. Custom binders
// can call it explicitly, and CRUD helpers independently enforce the same rules.
func PrepareAccessInput[T any](ctx context.Context, input modelbind.ModelInput[T], operation access.Operation) (modelbind.ModelInput[T], error) {
	if operation != access.Insert && operation != access.Update {
		return modelbind.ModelInput[T]{}, NormalizeAccessError(fmt.Errorf("%w: input preparation requires insert/update", ErrAccessConfiguration))
	}
	resolved, err := ResolveModelAccess(ctx, input.Model, operation)
	if err != nil {
		return modelbind.ModelInput[T]{}, err
	}
	return prepareResolvedInput(input, resolved, operation)
}

func prepareResolvedInput[T any](input modelbind.ModelInput[T], resolved ResolvedAccess, operation access.Operation) (modelbind.ModelInput[T], error) {
	if resolved.Applied {
		model, ok := any(input.Model).(types.DBModel)
		if !ok {
			return modelbind.ModelInput[T]{}, NormalizeAccessError(ErrAccessConfiguration)
		}
		// Check every row/write mapping before accepting HTTP input. Some rules
		// govern columns absent from the DTO; those stay on the SQL builder.
		var builder AccessPolicySetter
		if operation == access.Insert {
			builder = modelpg.NewPgInsert(model)
		} else {
			builder = modelpg.NewPgUpdate(model)
		}
		if err := resolved.ApplyTo(builder); err != nil {
			return modelbind.ModelInput[T]{}, err
		}
		md, err := metadata.NewModelMetadata(input.Model)
		if err != nil {
			return modelbind.ModelInput[T]{}, err
		}
		rules := make(access.WriteRules)
		for _, field := range resolved.Policy.Writes.Fields() {
			column := resolved.Binding.Writes[field]
			if _, presentInModel := md.Fields[column]; presentInModel {
				rules[column] = resolved.Policy.Writes[field]
			}
		}
		input, err = modelbind.PrepareModelInput(input, operation, rules)
		if err != nil {
			return modelbind.ModelInput[T]{}, NormalizeAccessError(err)
		}
	}
	if err := input.Validate(operation == access.Insert); err != nil {
		return modelbind.ModelInput[T]{}, err
	}
	return input, nil
}

// prepareBoundInput is deliberately also usable for non-DB inputs: legacy
// authentication/search DTOs retain their ordinary modelbind validation.
func prepareBoundInput[T any](ctx context.Context, input modelbind.ModelInput[T], forInsert bool) (modelbind.ModelInput[T], error) {
	operation := access.Update
	if forInsert {
		operation = access.Insert
	}
	return PrepareAccessInput(ctx, input, operation)
}
