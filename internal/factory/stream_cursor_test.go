package factory

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStreamReconnectCursorOverridesInitialQuery(t *testing.T) {
	s, _ := fixture(t)
	s.mu.Lock()
	foreman, e := s.foreman("project")
	if e != nil {
		s.mu.Unlock()
		t.Fatal(e)
	}
	for _, message := range []string{"one", "two", "three"} {
		if e = s.event(foreman.ID, Event{Kind: "message", Text: message}); e != nil {
			s.mu.Unlock()
			t.Fatal(e)
		}
	}
	events, e := s.events(foreman.ID, 0)
	s.mu.Unlock()
	if e != nil {
		t.Fatal(e)
	}
	for _, reconnect := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		request := httptest.NewRequest("GET", fmt.Sprintf("/events?cursor=%d", events[0].ID), nil).WithContext(ctx)
		if reconnect {
			request.Header.Set("Last-Event-ID", fmt.Sprint(events[1].ID))
		}
		response := httptest.NewRecorder()
		s.stream(response, request, foreman.ID)
		body := response.Body.String()
		if strings.Contains(body, fmt.Sprintf("id: %d\n", events[0].ID)) || !strings.Contains(body, fmt.Sprintf("id: %d\n", events[2].ID)) {
			t.Fatal("wrong event range", body)
		}
		second := strings.Contains(body, fmt.Sprintf("id: %d\n", events[1].ID))
		if second == reconnect {
			t.Fatal("reconnect cursor did not override initial query", body)
		}
	}
}
