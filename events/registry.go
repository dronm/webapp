package events

import "sync"

// UniqEvents keeps reference counts for PostgreSQL event channels.
type UniqEvents struct {
	mu sync.Mutex
	m  map[string]int
}

func NewUniqEvents() *UniqEvents {
	return &UniqEvents{m: map[string]int{}}
}

// Add increments the event reference counter. It returns true when the event
// was absent and the caller must execute LISTEN.
func (e *UniqEvents) Add(eventID string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.m == nil {
		e.m = map[string]int{}
	}

	if _, ok := e.m[eventID]; !ok {
		e.m[eventID] = 1
		return true
	}

	e.m[eventID]++
	return false
}

// Remove decrements the event reference counter. It returns true when the event
// reaches zero and the caller must execute UNLISTEN.
func (e *UniqEvents) Remove(eventID string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.m == nil {
		return false
	}

	count, ok := e.m[eventID]
	if !ok {
		return false
	}

	if count <= 1 {
		delete(e.m, eventID)
		return true
	}

	e.m[eventID] = count - 1
	return false
}

func (e *UniqEvents) EventCount(eventID string) (int, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()

	count, ok := e.m[eventID]
	return count, ok
}

func (e *UniqEvents) TotalEventCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()

	return len(e.m)
}

func (e *UniqEvents) EventIDs() []string {
	e.mu.Lock()
	defer e.mu.Unlock()

	out := make([]string, 0, len(e.m))
	for eventID := range e.m {
		out = append(out, eventID)
	}

	return out
}

func (e *UniqEvents) forceAdd(eventID string) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.m == nil {
		e.m = map[string]int{}
	}
	if _, ok := e.m[eventID]; !ok {
		e.m[eventID] = 1
	}
}
