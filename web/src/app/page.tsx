import Link from "next/link";
import { fetchEvents, fetchRuns, fetchStats, fetchWorkers, mode } from "@/lib/api";
import { evidenceOf, storyOf } from "@/lib/events";
import { duration, short, time } from "@/lib/format";
import { ErrorState } from "./error-state";
import { Status } from "./status";
import { Story } from "./story";

// The overview is the page someone leaves open while killing a worker, so it
// must reflect the database on every load rather than a cached render.
export const dynamic = "force-dynamic";

const REPO = "https://github.com/alexou8/relab";

export default async function Overview() {
  let data;
  try {
    const [stats, runs, workers] = await Promise.all([
      fetchStats(),
      fetchRuns(10),
      fetchWorkers(),
    ]);
    data = { stats, runs, workers };
  } catch (error) {
    return <ErrorState error={error} />;
  }

  const { stats, runs, workers } = data;
  const live = workers.filter(
    (w) => w.Status === "HEALTHY" || w.Status === "SUSPECT",
  ).length;
  const running = stats.runs_by_status["RUNNING"] ?? 0;

  // The run a first-time visitor should open. A run that was disrupted and
  // finished anyway is the whole argument in one page; without one, the newest
  // run is still better than nothing.
  const headline =
    runs.find((r) => r.ScenarioName !== "" && r.Status === "SUCCEEDED") ??
    runs.find((r) => r.ScenarioName !== "") ??
    runs[0];

  // The worked example on this page is a real run, read from the same API the
  // rest of the dashboard reads. If it cannot be loaded the page still stands;
  // it just makes its argument in prose instead of in evidence.
  let example = null;
  if (headline) {
    try {
      const events = await fetchEvents(headline.ID);
      const steps = storyOf(events);
      if (steps.length > 0) {
        example = { events, steps, evidence: evidenceOf(events) };
      }
    } catch {
      example = null;
    }
  }

  return (
    <>
      <section className="hero">
        <h2 className="hero-title">Break it. Watch it recover.</h2>
        <p className="hero-copy">
          ReLab is a reliability testing tool for workflows and background jobs.
          It runs a workflow across real worker processes, breaks it on purpose
          by killing a worker, delivering a task twice or holding an upstream
          down, and then records whether the system actually recovered.
        </p>
        <p className="hero-actions">
          {headline ? (
            <Link className="cta" href={`/runs/${headline.ID}`}>
              Explore a real failure
            </Link>
          ) : null}
          <a className="cta cta-quiet" href={REPO}>
            View on GitHub
          </a>
        </p>
        <p className="detail note">
          It is a reliability testing and replay tool. It is not a Temporal
          replacement. This dashboard is a read-only view of what the journal
          recorded; everything it shows is also available from <code>relab</code>{" "}
          on the command line.
        </p>
      </section>

      {example && headline ? (
        <section>
          <h2>One real failure, start to finish</h2>
          <p className="lead">
            {headline.ScenarioName === "worker-crash-after-effect"
              ? "A worker charged a customer, then was killed before it could acknowledge the task. Nobody told the system it had gone."
              : `ReLab disrupted the ${headline.WorkflowName} workflow on purpose and recorded what followed.`}{" "}
            Every step below is an event from that run&rsquo;s journal, at its
            real sequence number and its real time.
          </p>
          <Story steps={example.steps} />
          <Verdict
            recovered={headline.Status === "SUCCEEDED"}
            evidence={example.evidence}
            events={example.events.length}
          />
          <p className="note">
            <Link href={`/runs/${headline.ID}`}>
              Open the whole run, all {example.events.length} events
            </Link>{" "}
            <span className="detail">
              {headline.ScenarioName || headline.WorkflowName}
            </span>
          </p>
        </section>
      ) : null}

      <section>
        <h2>Why this exists</h2>
        <div className="explainer">
          <p>
            Workflow systems get tested on the path where nothing goes wrong.
            The paths that cost money are the ones that are hard to exercise on
            purpose:
          </p>
          <ul className="reasons">
            <li>A worker dies holding a task. Does the task come back?</li>
            <li>
              A hold on a task expires while its worker is still alive. Do two
              workers now run it?
            </li>
            <li>
              A retry repeats a step that already charged a customer. Does it
              charge twice?
            </li>
            <li>
              The coordinator restarts with work in flight. Does the work
              resume?
            </li>
          </ul>
          <p className="detail">
            &ldquo;We handle worker failure&rdquo; is either backed by a test
            that kills a real process, or it is a hope. ReLab is that test: the
            crashes are real <code>SIGKILL</code>s to real processes, and the
            faults degrade the real system rather than standing in for it.
          </p>
        </div>
      </section>

      <section>
        <h2>How a ReLab run works</h2>
        <ol className="pipeline">
          <PipelineStep n={1} title="Run a workflow">
            A multi-step workflow runs across worker processes, each task held
            by one worker at a time.
          </PipelineStep>
          <PipelineStep n={2} title="Break something">
            A scenario injects a real degradation from a fixed seed: a worker
            crash, a duplicate delivery, latency, an HTTP error, a database
            disconnect.
          </PipelineStep>
          <PipelineStep n={3} title="Record what happened">
            Every state change and the event describing it are written in one
            transaction, in a gapless sequence.
          </PipelineStep>
          <PipelineStep n={4} title="Replay the history">
            A pure reducer rebuilds the run&rsquo;s state from the journal
            alone, with no access to the database it came from.
          </PipelineStep>
          <PipelineStep n={5} title="Verify the result">
            Assertions check the recovery: the task came back, the side effect
            was not repeated, the run reached the state the journal says it
            reached.
          </PipelineStep>
        </ol>
        <p className="detail note">
          Underneath: PostgreSQL as the only datastore and the coordination
          point, task leases renewed by the worker holding them, a reaper that
          releases the leases of workers that stopped answering, an idempotency
          ledger keyed per effect, and an append-only event log.{" "}
          <a href={`${REPO}/blob/main/docs/reliability.md`}>
            The guarantees, stated precisely
          </a>
          .
        </p>
      </section>

      <section>
        <h2>Now</h2>
        <div className="counts">
          <Count label="Running" value={running} />
          <Count label="Queue depth" value={stats.queue_depth} />
          <Count label="Live workers" value={live} />
          <Count label="Dead letters" value={stats.dead_letters} />
        </div>
        <p className="detail note">
          A live worker is one that is HEALTHY or SUSPECT: still heartbeating,
          or doubted but not yet reclaimed. A dead letter is a task that
          exhausted its attempts.
          {mode() === "demo"
            ? " Nothing here is running: these are the counters as they stood when the recording was made."
            : null}
        </p>
      </section>

      <section>
        <h2>Latest runs</h2>
        {runs.length === 0 ? (
          <p className="empty">
            Nothing has run yet. Start one with{" "}
            <code>relab run examples/data-pipeline.yaml</code>.
          </p>
        ) : (
          <div className="table-scroll">
            <table>
              <caption className="sr-only">
                The {runs.length} most recent runs, newest first.
              </caption>
              <thead>
                <tr>
                  <th scope="col">Status</th>
                  <th scope="col">Workflow</th>
                  <th scope="col">Run</th>
                  <th scope="col">What was broken</th>
                  <th scope="col" className="mono">
                    Started
                  </th>
                  <th scope="col" className="mono">
                    Duration
                  </th>
                </tr>
              </thead>
              <tbody>
                {runs.map((run) => (
                  <tr key={run.ID}>
                    <td>
                      <Status value={run.Status} />
                    </td>
                    <th scope="row" className="row-name">
                      {run.WorkflowName}{" "}
                      <span className="detail">v{run.WorkflowVer}</span>
                    </th>
                    <td className="mono">
                      <Link href={`/runs/${run.ID}`}>{short(run.ID)}</Link>
                    </td>
                    <td>
                      {run.ScenarioName || (
                        <span className="detail">nothing</span>
                      )}
                    </td>
                    <td className="mono">{time(run.CreatedAt)}</td>
                    <td className="mono">
                      {duration(run.CreatedAt, run.CompletedAt) ?? "·"}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>

      <section>
        <h2>Go deeper</h2>
        <ul className="links">
          <li>
            <a href={`${REPO}#readme`}>README</a>{" "}
            <span className="detail">what it is, and a demo you can run</span>
          </li>
          <li>
            <a href={`${REPO}/blob/main/ARCHITECTURE.md`}>Architecture</a>{" "}
            <span className="detail">
              engine, workers, replay, fault injection
            </span>
          </li>
          <li>
            <a href={`${REPO}/blob/main/docs/reliability.md`}>
              Reliability guarantees
            </a>{" "}
            <span className="detail">
              stated precisely, with the limitations kept
            </span>
          </li>
          <li>
            <a href={`${REPO}/blob/main/docs/benchmarks.md`}>Benchmarks</a>{" "}
            <span className="detail">measured, on stated hardware</span>
          </li>
          <li>
            <a href={`${REPO}/tree/main/docs/decisions`}>Decision records</a>{" "}
            <span className="detail">
              what was decided, and what was rejected
            </span>
          </li>
        </ul>
      </section>
    </>
  );
}

/**
 * The result of the worked example, said as plainly as the journal allows.
 *
 * Each line is a count of events, not a claim: "no duplicate effect" is the
 * absence of a second effect under the same key, which is what the ledger
 * records. It never says exactly-once, because the engine does not provide it.
 */
function Verdict({
  recovered,
  evidence,
  events,
}: {
  recovered: boolean;
  evidence: ReturnType<typeof evidenceOf>;
  events: number;
}) {
  return (
    <div className={recovered ? "verdict verdict-ok" : "verdict verdict-bad"}>
      <p className="verdict-head">
        <span className="verdict-word">
          {recovered ? "RECOVERED" : "DID NOT RECOVER"}
        </span>
        <span className="detail">from {events} recorded events</span>
      </p>
      <ul className="verdict-facts">
        <li>
          <span className="k">Workflow completed</span>
          <span className="v">{recovered ? "yes" : "no"}</span>
        </li>
        <li>
          <span className="k">Tasks abandoned</span>
          <span className="v">{evidence.deadLettered}</span>
        </li>
        <li>
          <span className="k">Duplicate effects prevented</span>
          <span className="v">{evidence.effectsSuppressed}</span>
        </li>
        <li>
          <span className="k">Attempts run</span>
          <span className="v">{evidence.attempts}</span>
        </li>
        <li>
          <span className="k">Time to recover</span>
          <span className="v">
            {evidence.recoveryMs === null
              ? "·"
              : `${(evidence.recoveryMs / 1000).toFixed(2)}s`}
          </span>
        </li>
      </ul>
    </div>
  );
}

function PipelineStep({
  n,
  title,
  children,
}: {
  n: number;
  title: string;
  children: React.ReactNode;
}) {
  return (
    <li className="pipeline-step">
      <span className="pipeline-n mono">{n}</span>
      <div>
        <h3>{title}</h3>
        <p>{children}</p>
      </div>
    </li>
  );
}

function Count({ label, value }: { label: string; value: number }) {
  return (
    <div className="count">
      <div className="value">{value}</div>
      <div className="label">{label}</div>
    </div>
  );
}
