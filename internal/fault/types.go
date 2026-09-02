package fault

// Type names a fault this build can inject.
type Type string

// The v1 fault types. Each corresponds to a real failure mode of a distributed
// worker pool, and each is injected by killing or degrading something real
// rather than by setting a flag the scheduler consults.
const (
	// WorkerCrash kills the worker process outright, with no chance to release
	// its lease. Recovery has to come from lease expiry.
	WorkerCrash Type = "worker-crash"
	// DuplicateDelivery executes an already-claimed task a second time,
	// exercising the idempotency ledger.
	DuplicateDelivery Type = "duplicate-delivery"
	// Latency delays a handler, pushing it towards its lease and its timeout.
	Latency Type = "latency"
	// HTTPError makes an outbound call fail with a configurable status.
	HTTPError Type = "http-error"
	// DBDisconnect closes the worker's database connections mid-task.
	DBDisconnect Type = "db-disconnect"
	// QueueOverload floods the queue so that claims contend.
	//
	// It is DECLARED BUT NOT IMPLEMENTED in v1, and scenarios using it are
	// rejected rather than silently running without it. Queue contention is a
	// property of the whole pool rather than of one task, so it does not fit
	// the per-run, per-task trigger-point model the other faults use, and
	// bolting it on would have produced a fault that fires somewhere unrelated
	// to where a scenario says it does. Accepting a scenario that does nothing
	// would be worse than refusing it: it would report a passing reliability
	// test that never ran.
	QueueOverload Type = "queue-overload"
)

// knownTypes is what this build can actually inject. QueueOverload is
// deliberately absent; see its declaration.
var knownTypes = map[Type]struct{}{
	WorkerCrash: {}, DuplicateDelivery: {}, Latency: {},
	HTTPError: {}, DBDisconnect: {},
}

// declaredButUnimplemented gives a scenario author a better error than "unknown
// type" for a fault this build names but cannot inject.
var declaredButUnimplemented = map[Type]string{
	QueueOverload: "queue contention is a property of the whole pool rather than of one " +
		"task, so it does not fit the trigger-point model; it is planned, not shipped",
}

// Unimplemented reports whether t is a fault type ReLab names but does not yet
// inject, and why.
func Unimplemented(t Type) (string, bool) {
	reason, ok := declaredButUnimplemented[t]
	return reason, ok
}

// Known reports whether t is a fault type this build implements.
func (t Type) Known() bool {
	_, ok := knownTypes[t]
	return ok
}

func (t Type) String() string { return string(t) }

// TypeNames lists the implemented fault types, for error messages.
func TypeNames() []string {
	return []string{
		string(WorkerCrash), string(DuplicateDelivery), string(Latency),
		string(HTTPError), string(DBDisconnect),
	}
}

// Point names a place in the task lifecycle where a fault can be triggered.
// The points are the boundaries where behaviour actually differs: before a
// claim, after it, around the handler, and around the acknowledgement. A fault
// injected "somewhere in the middle" would not be reproducible.
type Point string

// The trigger points.
const (
	BeforeClaim     Point = "before-claim"
	AfterTaskLease  Point = "after-task-lease"
	AfterTaskStart  Point = "after-task-start"
	BeforeTaskAck   Point = "before-task-ack"
	AfterTaskFinish Point = "after-task-finish"
)

var knownPoints = map[Point]struct{}{
	BeforeClaim: {}, AfterTaskLease: {}, AfterTaskStart: {},
	BeforeTaskAck: {}, AfterTaskFinish: {},
}

// Known reports whether p is a trigger point this build implements.
func (p Point) Known() bool {
	_, ok := knownPoints[p]
	return ok
}

func (p Point) String() string { return string(p) }

// PointNames lists the trigger points, for error messages.
func PointNames() []string {
	return []string{
		string(BeforeClaim), string(AfterTaskLease), string(AfterTaskStart),
		string(BeforeTaskAck), string(AfterTaskFinish),
	}
}
