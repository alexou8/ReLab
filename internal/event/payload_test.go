package event_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/alexou8/relab/internal/event"
)

func TestEncodeStampsVersion(t *testing.T) {
	raw, err := event.Encode(event.TaskFailedPayload{Attempt: 2, Error: "boom", Retryable: true})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded["v"] != float64(event.PayloadVersion) {
		t.Fatalf("payload version is %v, want %d", decoded["v"], event.PayloadVersion)
	}
	if decoded["error"] != "boom" {
		t.Fatalf("payload lost its fields: %v", decoded)
	}
}

func TestDecodeRejectsUnversionedPayload(t *testing.T) {
	var p event.TaskFailedPayload
	err := event.Decode(json.RawMessage(`{"error":"boom"}`), &p)
	if err == nil || !strings.Contains(err.Error(), "no version field") {
		t.Fatalf("decode returned %v, want a missing-version error", err)
	}
}

func TestDecodeRejectsFutureVersion(t *testing.T) {
	var p event.TaskFailedPayload
	err := event.Decode(json.RawMessage(`{"v":99,"error":"boom"}`), &p)
	if err == nil || !strings.Contains(err.Error(), "not supported by this build") {
		t.Fatalf("decode returned %v, want an unsupported-version error", err)
	}
}

func TestDecodeRoundTrip(t *testing.T) {
	in := event.TaskRetryScheduledPayload{Attempt: 1, NextAttempt: 2, DelayMS: 1500}
	raw, err := event.Encode(in)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var out event.TaskRetryScheduledPayload
	if err := event.Decode(raw, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Attempt != in.Attempt || out.NextAttempt != in.NextAttempt || out.DelayMS != in.DelayMS {
		t.Fatalf("round trip changed the payload: %+v -> %+v", in, out)
	}
}

func TestKnownTypesAreExhaustive(t *testing.T) {
	// Every payload type declared in this package must map to a known event
	// type. A payload whose Type() is not in knownTypes cannot be appended, and
	// the failure would otherwise only show up at run time.
	payloads := []event.Payload{
		event.RunCreatedPayload{}, event.RunQueuedPayload{}, event.RunStartedPayload{},
		event.RunSucceededPayload{}, event.RunFailedPayload{}, event.RunCancelledPayload{},
		event.TaskScheduledPayload{}, event.TaskLeasedPayload{}, event.TaskStartedPayload{},
		event.TaskSucceededPayload{}, event.TaskFailedPayload{}, event.TaskRetryScheduledPayload{},
		event.TaskLeaseExpiredPayload{}, event.TaskRequeuedPayload{}, event.TaskDeadLetteredPayload{},
		event.WorkerRegisteredPayload{}, event.WorkerHeartbeatPayload{},
		event.WorkerSuspectPayload{}, event.WorkerLostPayload{},
		event.FaultInjectedPayload{}, event.SideEffectSkippedPayload{},
	}
	if len(payloads) != 21 {
		t.Fatalf("this test lists %d payloads; the journal defines 21", len(payloads))
	}
	for _, p := range payloads {
		if !p.Type().Known() {
			t.Errorf("payload %T reports type %q, which is not a known event type", p, p.Type())
		}
	}
}
