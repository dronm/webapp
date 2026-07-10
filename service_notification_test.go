package webapp

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
)

type testPublishedEvent struct {
	PublisherID string
	EventID     string
	Payload     any
}

type testEventPublisher struct {
	Events []testPublishedEvent
	Err    error
}

func (p *testEventPublisher) PublishEvent(publisherID string, eventID string, payload any) error {
	p.Events = append(p.Events, testPublishedEvent{
		PublisherID: publisherID,
		EventID:     eventID,
		Payload:     payload,
	})

	return p.Err
}

type notificationTestService struct{}

func newNotificationTestService(ctx ServiceContext) any {
	return &notificationTestService{}
}

func (s *notificationTestService) Create(ctx context.Context, input int) (map[string]any, error) {
	if input < 0 {
		return nil, errors.New("create failed")
	}

	return map[string]any{"id": input}, nil
}

func (s *notificationTestService) Update(ctx context.Context, input int) error {
	return nil
}

func (s *notificationTestService) List(ctx context.Context) ([]int, error) {
	return []int{1}, nil
}

func TestCallServiceMethodPublishesConfiguredCRUDNotification(t *testing.T) {
	resetTestServices(t)

	if err := RegisterService(
		"Item",
		&notificationTestService{},
		newNotificationTestService,
		WithCRUDNotifications(),
	); err != nil {
		t.Fatalf("RegisterService() error = %v", err)
	}

	publisher := &testEventPublisher{}
	result, err := CallServiceMethod(
		context.Background(),
		"Item",
		"Create",
		42,
		ServiceContext{
			EventPublisher: publisher,
			QueryID:        "query-42",
		},
	)
	if err != nil {
		t.Fatalf("CallServiceMethod() error = %v", err)
	}

	resultMap, ok := result.(map[string]any)
	if !ok || resultMap["id"] != 42 {
		t.Fatalf("result = %#v, want id 42", result)
	}

	if len(publisher.Events) != 1 {
		t.Fatalf("published events = %d, want 1", len(publisher.Events))
	}

	event := publisher.Events[0]
	if event.PublisherID != "" {
		t.Fatalf("publisher id = %q, want empty", event.PublisherID)
	}
	if event.EventID != "Item.Create" {
		t.Fatalf("event id = %q, want Item.Create", event.EventID)
	}

	payload, ok := event.Payload.(ServiceMethodEvent)
	if !ok {
		t.Fatalf("payload type = %T, want ServiceMethodEvent", event.Payload)
	}
	if payload.Input != 42 {
		t.Fatalf("payload input = %#v, want 42", payload.Input)
	}
	if payload.QueryID != "query-42" {
		t.Fatalf("payload query id = %q, want query-42", payload.QueryID)
	}
	if payload.Result == nil {
		t.Fatal("payload result is nil")
	}
}

func TestCallServiceMethodDoesNotPublishOnFailureOrUnconfiguredMethod(t *testing.T) {
	resetTestServices(t)

	if err := RegisterService(
		"Item",
		&notificationTestService{},
		newNotificationTestService,
		WithCRUDNotifications(),
	); err != nil {
		t.Fatalf("RegisterService() error = %v", err)
	}

	publisher := &testEventPublisher{}
	_, err := CallServiceMethod(
		context.Background(),
		"Item",
		"Create",
		-1,
		ServiceContext{EventPublisher: publisher},
	)
	if err == nil {
		t.Fatal("CallServiceMethod() error = nil, want failure")
	}

	_, err = CallServiceMethod(
		context.Background(),
		"Item",
		"List",
		nil,
		ServiceContext{EventPublisher: publisher},
	)
	if err != nil {
		t.Fatalf("CallServiceMethod(List) error = %v", err)
	}

	if len(publisher.Events) != 0 {
		t.Fatalf("published events = %d, want 0", len(publisher.Events))
	}
}

func TestCallServiceMethodIgnoresPublisherFailure(t *testing.T) {
	resetTestServices(t)

	if err := RegisterService(
		"Item",
		&notificationTestService{},
		newNotificationTestService,
		WithCRUDNotifications(),
	); err != nil {
		t.Fatalf("RegisterService() error = %v", err)
	}

	publisher := &testEventPublisher{Err: errors.New("publish failed")}
	_, err := CallServiceMethod(
		context.Background(),
		"Item",
		"Update",
		42,
		ServiceContext{EventPublisher: publisher},
	)
	if err != nil {
		t.Fatalf("CallServiceMethod() error = %v, want nil", err)
	}

	if len(publisher.Events) != 1 {
		t.Fatalf("published events = %d, want 1", len(publisher.Events))
	}
}

func TestNewServiceContextIncludesEventPublisher(t *testing.T) {
	publisher := &testEventPublisher{}
	req := httptest.NewRequest("GET", "/", nil)

	svcCtx := NewServiceContext(req, &AppContext{
		EventPublisher: publisher,
	})

	if svcCtx.EventPublisher != publisher {
		t.Fatal("service context event publisher was not propagated")
	}
}
