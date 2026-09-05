import Link from "next/link";
import {
  PHRASE_GROUPS,
  STATUS_MEANING,
  UNGROUPED_TYPES,
  UNJOURNALLED_TYPES,
  phraseOf,
} from "@/lib/vocab";

export const metadata = {
  title: "Glossary — ReLab",
  description:
    "What each event in a ReLab journal means, and the words the rest of the dashboard uses.",
};

const REPO = "https://github.com/alexou8/relab";

// The vocabulary of the system, not of a run. Nothing here reads the API, so
// it renders identically against a live control plane and against the
// recording.
export const dynamic = "force-static";

/** The terms a reader meets on the overview before anything defines them. */
const TERMS: { term: string; definition: React.ReactNode }[] = [
  {
    term: "Workflow",
    definition:
      "A named set of steps and the dependencies between them. It is a definition; it does not run on its own.",
  },
  {
    term: "Run",
    definition:
      "One execution of a workflow. Its history is the journal, and its last event is terminal: once a run has finished, its story cannot change.",
  },
  {
    term: "Task",
    definition:
      "One step of a run. A task can be attempted several times; each attempt is recorded separately.",
  },
  {
    term: "Worker",
    definition:
      "A process that claims tasks and runs their handlers. ReLab's crash tests kill real worker processes with SIGKILL.",
  },
  {
    term: "Lease",
    definition: (
      <>
        A time-bounded hold on a task. One worker holds it, and renews it while
        it works. If the renewals stop, the hold expires — which is what makes
        recovery work when a machine loses power and nothing gets the chance to
        say so.
      </>
    ),
  },
  {
    term: "Reaper",
    definition:
      "The part of the engine that expires leases nobody renewed and returns that work to the queue. It is why a killed worker's task comes back without anyone reporting the death.",
  },
  {
    term: "Heartbeat",
    definition:
      "A worker saying it is alive. Three missed beats make it SUSPECT; five make it LOST and release its leases. One missed beat is never treated as failure.",
  },
  {
    term: "Journal",
    definition:
      "The append-only event history of a run. A state change and the event describing it are written in one transaction, and sequence numbers are gapless — so a gap means data was lost, not that something was skipped.",
  },
  {
    term: "Replay",
    definition:
      "Rebuilding a run's state from its journal alone, with a pure reducer that cannot read the database. If replay and the database disagree, the journal is not a faithful account and that is a bug worth finding.",
  },
  {
    term: "Idempotency key",
    definition: (
      <>
        The name a handler gives a side effect — a charge, an email — so the
        system can tell whether it has already been recorded. A retry that asks
        for an effect already recorded under the same key does not perform it
        again.
      </>
    ),
  },
  {
    term: "Dead letter",
    definition:
      "A task that exhausted its attempts. It is kept, not discarded: an abandoned task is evidence, and the run's failure has to be explainable afterwards.",
  },
  {
    term: "Scenario",
    definition:
      "A run with a fault injected on purpose, from a fixed seed, so the same break can be reproduced. The fault degrades the real system rather than simulating a degradation.",
  },
  {
    term: "At-least-once",
    definition: (
      <>
        What ReLab actually guarantees: a task may run more than once, and an
        effect already <em>recorded</em> under a key is not performed again.
        There is a window between performing an effect and recording it in which
        a crash can cost a duplicate. ReLab does not claim exactly-once, because
        it does not have it —{" "}
        <a href={`${REPO}/blob/main/docs/decisions/0005-at-least-once-not-exactly-once.md`}>
          decision 0005
        </a>{" "}
        says why.
      </>
    ),
  },
];

export default function Glossary() {
  return (
    <>
      <section className="hero">
        <h2 className="hero-title">Glossary</h2>
        <p className="hero-copy">
          Every event in a ReLab journal, in the words a first-time reader would
          use, next to the event type an engineer will check it against. The
          type is the evidence and is never replaced.
        </p>
      </section>

      <section>
        <h2>Terms</h2>
        <dl className="glossary">
          {TERMS.map(({ term, definition }) => (
            <div className="gterm" key={term}>
              <dt>{term}</dt>
              <dd>{definition}</dd>
            </div>
          ))}
        </dl>
      </section>

      {PHRASE_GROUPS.map((group) => (
        <section key={group.heading}>
          <h2>{group.heading}</h2>
          <p className="note">{group.blurb}</p>
          <dl className="glossary">
            {group.types.map((type) => {
              const phrase = phraseOf(type);
              if (!phrase) return null;
              return (
                <div className="gterm" key={type}>
                  <dt>
                    {phrase.label} <code className="gtype">{type}</code>
                  </dt>
                  <dd>
                    {phrase.meaning}
                    {UNJOURNALLED_TYPES.has(type) ? (
                      <>
                        {" "}
                        <span className="gnote">
                          Defined, but not written to a run journal today — this
                          one is state in the workers table, so you will not
                          find it on a run&rsquo;s timeline.
                        </span>
                      </>
                    ) : null}
                  </dd>
                </div>
              );
            })}
          </dl>
        </section>
      ))}

      {UNGROUPED_TYPES.length > 0 ? (
        <section>
          <h2>Not yet grouped</h2>
          <p className="note">
            These event types have a plain-language reading but no place in the
            groups above. That is a gap in this page, not in the journal.
          </p>
          <dl className="glossary">
            {UNGROUPED_TYPES.map((type) => (
              <div className="gterm" key={type}>
                <dt>
                  {phraseOf(type)?.label} <code className="gtype">{type}</code>
                </dt>
                <dd>{phraseOf(type)?.meaning}</dd>
              </div>
            ))}
          </dl>
        </section>
      ) : null}

      <section>
        <h2>Statuses</h2>
        <p className="note">
          What a run, task, or worker status means, in one clause. The same
          words appear on every table in the dashboard.
        </p>
        <dl className="glossary glossary-tight">
          {Object.entries(STATUS_MEANING).map(([status, meaning]) => (
            <div className="gterm" key={status}>
              <dt>
                <code className="gtype">{status}</code>
              </dt>
              <dd>{meaning}</dd>
            </div>
          ))}
        </dl>
      </section>

      <section>
        <h2>Where these are defined</h2>
        <ul className="links">
          <li>
            <a href={`${REPO}/blob/main/DATA.md`}>DATA.md</a>{" "}
            <span className="detail">
              the schema as implemented, including every event type
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
            <Link href="/">Back to the overview</Link>{" "}
            <span className="detail">a real run, start to finish</span>
          </li>
        </ul>
      </section>
    </>
  );
}
