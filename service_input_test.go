package webapp

import (
	"encoding/json"
	"testing"
)

func TestDecodeInputForServiceScalarInput(t *testing.T) {
	resetTestServices(t)
	registerTestService(t, "Supplier")

	input, err := DecodeInputForService("Supplier", "Detail", json.RawMessage(`42`))
	if err != nil {
		t.Fatalf("DecodeInputForService() error = %v", err)
	}

	id, ok := input.(int)
	if !ok {
		t.Fatalf("input type = %T, want int", input)
	}
	if id != 42 {
		t.Fatalf("input = %d, want 42", id)
	}
}

func TestDecodeInputForServiceRejectsPayloadForNoInputMethod(t *testing.T) {
	resetTestServices(t)
	registerTestService(t, "Supplier")

	_, err := DecodeInputForService("Supplier", "NoInput", json.RawMessage(`{"unexpected":true}`))
	if err == nil {
		t.Fatalf("DecodeInputForService() error = nil, want error")
	}
}

func TestDecodeInputForServiceAllowsNullForNoInputMethod(t *testing.T) {
	resetTestServices(t)
	registerTestService(t, "Supplier")

	input, err := DecodeInputForService("Supplier", "NoInput", json.RawMessage(`null`))
	if err != nil {
		t.Fatalf("DecodeInputForService() error = %v", err)
	}
	if input != nil {
		t.Fatalf("input = %#v, want nil", input)
	}
}
