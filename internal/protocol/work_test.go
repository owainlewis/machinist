package protocol

import "testing"

func TestWorkUpdateTypesAcceptOnlyProtocolValues(t *testing.T) {
	for _, status := range []WorkUpdateStatus{
		WorkUpdateRunning, WorkUpdateReady, WorkUpdateNeedsInput, WorkUpdateFailed, WorkUpdateNoChange,
	} {
		if !SupportedWorkUpdateStatus(status) {
			t.Fatalf("supported status %q was rejected", status)
		}
	}
	for _, status := range []WorkUpdateStatus{"queued", "succeeded", "cancelled", "misspelled"} {
		if SupportedWorkUpdateStatus(status) {
			t.Fatalf("unsupported status %q was accepted", status)
		}
	}
	for _, actor := range []WorkUpdateActor{
		WorkUpdateActorAgent, WorkUpdateActorOperator, WorkUpdateActorSystem,
	} {
		if !SupportedWorkUpdateActor(actor) {
			t.Fatalf("supported actor %q was rejected", actor)
		}
	}
	for _, actor := range []WorkUpdateActor{"worker", "human", "misspelled"} {
		if SupportedWorkUpdateActor(actor) {
			t.Fatalf("unsupported actor %q was accepted", actor)
		}
	}
}
