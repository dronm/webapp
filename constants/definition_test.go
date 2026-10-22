package constants

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

func TestRegisteredDefinitionValidatesJSONStructureAndBusinessRules(t *testing.T) {
	type priceSettings struct {
		PriceTypeID int64 `json:"price_type_id"`
		Enabled     bool  `json:"enabled"`
	}
	registry := NewRegistry()
	businessError := errors.New("price type must be positive")
	if err := Register(registry, Definition[priceSettings]{
		Code: "default_price_type", ValueType: TypeJSON, Required: true,
		Validate: func(_ context.Context, value priceSettings) error {
			if value.PriceTypeID <= 0 {
				return businessError
			}
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	definition := registry.snapshot()["default_price_type"]
	if err := definition.validate(context.Background(), json.RawMessage(`{"price_type_id":9223372036854775807,"enabled":true}`)); err != nil {
		t.Fatalf("maximum int64 should decode exactly: %v", err)
	}
	for _, raw := range []string{
		`{"price_type_id":1,"extra":true}`,
		`{"price_type_id":1.5}`,
		`{"price_type_id":9223372036854775808}`,
		`{"price_type_id":1} {"price_type_id":2}`,
		`null`,
	} {
		if err := definition.validate(context.Background(), json.RawMessage(raw)); !errors.Is(err, ErrValidation) {
			t.Errorf("%s: wanted validation error, got %v", raw, err)
		}
	}
	if err := definition.validate(context.Background(), json.RawMessage(`{"enabled":true}`)); !errors.Is(err, businessError) || !errors.Is(err, ErrValidation) {
		t.Fatalf("missing business-required field should preserve both errors: %v", err)
	}
}

func TestLogicalTypeValidationPreservesPrecision(t *testing.T) {
	tests := []struct {
		name      string
		valueType ValueType
		allowNull bool
		raw       string
		valid     bool
	}{
		{"int64 max", TypeInt64, false, `9223372036854775807`, true},
		{"int64 min", TypeInt64, false, `-9223372036854775808`, true},
		{"int64 overflow", TypeInt64, false, `9223372036854775808`, false},
		{"integer fraction", TypeInt64, false, `1.25`, false},
		{"integer exponent", TypeInt64, false, `1e3`, false},
		{"integer string", TypeInt64, false, `"42"`, false},
		{"exact decimal", TypeDecimal, false, `"12345678901234567890.0000000001"`, true},
		{"decimal number", TypeDecimal, false, `1.25`, false},
		{"decimal exponent", TypeDecimal, false, `"1e3"`, false},
		{"decimal leading zeros", TypeDecimal, false, `"01.25"`, false},
		{"decimal negative", TypeDecimal, false, `"-0.125"`, true},
		{"float overflow", TypeFloat64, false, `1e10000`, false},
		{"float exponent", TypeFloat64, false, `1.25e3`, true},
		{"float boolean", TypeFloat64, false, `true`, false},
		{"boolean", TypeBool, false, `false`, true},
		{"boolean number", TypeBool, false, `0`, false},
		{"string", TypeString, false, `"hello"`, true},
		{"string object", TypeString, false, `{}`, false},
		{"null denied", TypeJSON, false, `null`, false},
		{"null allowed", TypeJSON, true, `null`, true},
		{"empty value", TypeJSON, false, ``, false},
		{"trailing data", TypeJSON, false, `{} []`, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateRawType(test.valueType, test.allowNull, json.RawMessage(test.raw))
			if (err == nil) != test.valid {
				t.Fatalf("valid=%t, got %v", test.valid, err)
			}
		})
	}
}

func TestRegistryRejectsUnsafeOrMismatchedDefinitions(t *testing.T) {
	registry := NewRegistry()
	if err := Register(registry, Definition[bool]{Code: "flag", ValueType: TypeBool}); err != nil {
		t.Fatal(err)
	}
	if err := Register(registry, Definition[bool]{Code: "flag", ValueType: TypeBool}); !errors.Is(err, ErrDefinition) {
		t.Fatalf("duplicate code: %v", err)
	}
	if err := Register(registry, Definition[string]{Code: "wrong_type", ValueType: TypeBool}); !errors.Is(err, ErrDefinition) {
		t.Fatalf("wrong type: %v", err)
	}
	if err := Register(registry, Definition[any]{Code: "dynamic", ValueType: TypeJSON}); !errors.Is(err, ErrDefinition) {
		t.Fatalf("interface type: %v", err)
	}
	if err := Register(registry, Definition[map[string]any]{Code: "dynamic_map", ValueType: TypeJSON}); !errors.Is(err, ErrDefinition) {
		t.Fatalf("nested interface type: %v", err)
	}
	if err := Register(registry, Definition[bool]{Code: "null_flag", ValueType: TypeBool, AllowNull: true}); !errors.Is(err, ErrDefinition) {
		t.Fatalf("null for nonnullable type: %v", err)
	}
	if err := Register(registry, Definition[*bool]{Code: "optional_flag", ValueType: TypeBool, AllowNull: true}); err != nil {
		t.Fatalf("nullable pointer: %v", err)
	}
	if err := registry.snapshot()["optional_flag"].validate(context.Background(), json.RawMessage(`null`)); err != nil {
		t.Fatalf("explicitly nullable value: %v", err)
	}
	if err := Register(registry, Definition[json.RawMessage]{Code: "raw", ValueType: TypeJSON}); err != nil {
		t.Fatalf("raw JSON escape hatch: %v", err)
	}
	if err := Register(registry, Definition[bool]{Code: "invalid_editor", ValueType: TypeBool, Editor: json.RawMessage(`{"broken"`)}); !errors.Is(err, ErrDefinition) {
		t.Fatalf("invalid editor JSON: %v", err)
	}
}

func TestRegistrySnapshotOwnsEditorBytesAndHonorsCancellation(t *testing.T) {
	registry := NewRegistry()
	editor := json.RawMessage(`{"label":"A"}`)
	if err := Register(registry, Definition[string]{Code: "name", ValueType: TypeString, Editor: editor}); err != nil {
		t.Fatal(err)
	}
	editor[10] = 'B'
	snapshot := registry.snapshot()
	if got := string(snapshot["name"].editor); got != `{"label":"A"}` {
		t.Fatalf("caller mutated registered editor bytes: %s", got)
	}
	modified := snapshot["name"]
	modified.editor[10] = 'C'
	if got := string(registry.snapshot()["name"].editor); got != `{"label":"A"}` {
		t.Fatalf("snapshot mutated original editor bytes: %s", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := registry.snapshot()["name"].validate(ctx, json.RawMessage(`"value"`)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation must be preserved: %v", err)
	}
}

type definitionCustomDecimal struct{ text string }

func (d *definitionCustomDecimal) UnmarshalJSON(raw []byte) error {
	return json.Unmarshal(raw, &d.text)
}

func (d definitionCustomDecimal) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.text)
}

func TestCustomJSONCodecReceivesValidatedWireType(t *testing.T) {
	registry := NewRegistry()
	if err := Register(registry, Definition[definitionCustomDecimal]{
		Code: "rate", ValueType: TypeDecimal,
		Validate: func(_ context.Context, value definitionCustomDecimal) error {
			if value.text != "123.4500" {
				return fmt.Errorf("precision was lost: %s", value.text)
			}
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	definition := registry.snapshot()["rate"]
	if err := definition.validate(context.Background(), json.RawMessage(`"123.4500"`)); err != nil {
		t.Fatal(err)
	}
	if err := definition.validate(context.Background(), json.RawMessage(`123.45`)); !errors.Is(err, ErrValidation) {
		t.Fatalf("custom decoder must not bypass the logical wire type: %v", err)
	}
}
