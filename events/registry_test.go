package events

import "testing"

func TestUniqEventsReferenceCounting(t *testing.T) {
	events := NewUniqEvents()

	if !events.Add("Order.Changed") {
		t.Fatalf("first Add() returned false, want true")
	}
	if events.Add("Order.Changed") {
		t.Fatalf("second Add() returned true, want false")
	}

	count, ok := events.EventCount("Order.Changed")
	if !ok || count != 2 {
		t.Fatalf("count = %d, ok = %v, want 2 true", count, ok)
	}

	if events.Remove("Order.Changed") {
		t.Fatalf("first Remove() returned true, want false")
	}
	if !events.Remove("Order.Changed") {
		t.Fatalf("second Remove() returned false, want true")
	}

	if _, ok := events.EventCount("Order.Changed"); ok {
		t.Fatalf("event still exists after refcount reached zero")
	}
}
