/**
 * How a killed worker's task comes back, as a picture.
 *
 * Drawn in HTML rather than SVG on purpose: the stages have to reflow from a
 * row into a column on a phone, and text inside a scaled SVG viewBox would be
 * six pixels tall by the time it fit. Each stage names the event type that
 * records it, so the picture and the journal can be checked against each other
 * rather than taken on trust.
 */

const STAGES: {
  title: string;
  detail: string;
  type: string;
  tone: "plain" | "break" | "recovery" | "ok";
}[] = [
  {
    title: "A worker holds the task",
    detail:
      "The worker holds a lease and renews it while it works. It performs the workflow's charge step — a recorded effect standing in for a payment — and writes it to the ledger under a key.",
    type: "TASK_LEASED",
    tone: "plain",
  },
  {
    title: "The worker is killed",
    detail:
      "SIGKILL, mid-task. No shutdown, no handover, nothing gets the chance to report it. The renewals simply stop.",
    type: "FAULT_INJECTED",
    tone: "break",
  },
  {
    title: "The hold expires",
    detail:
      "Nobody renewed the lease, so the reaper concludes the holder is gone. This is the same path a machine losing power takes.",
    type: "TASK_LEASE_EXPIRED",
    tone: "recovery",
  },
  {
    title: "The task comes back",
    detail:
      "The work is claimable again, and another worker takes it under a new attempt number. Nobody had to notice the death for this to happen.",
    type: "TASK_REQUEUED",
    tone: "recovery",
  },
  {
    title: "The charge is not repeated",
    detail:
      "The retry asks for an effect already recorded under that key, so it is not performed a second time.",
    type: "SIDE_EFFECT_SKIPPED",
    tone: "recovery",
  },
  {
    title: "The run finishes",
    detail:
      "Every task done, the whole thing recorded in one gapless sequence that replay can rebuild without the database.",
    type: "RUN_SUCCEEDED",
    tone: "ok",
  },
];

export function RecoveryDiagram() {
  return (
    <ol className="flow" aria-label="How a killed worker's task recovers">
      {STAGES.map((stage, i) => (
        <li key={stage.type} className={`flow-stage flow-${stage.tone}`}>
          <span className="flow-n mono" aria-hidden="true">
            {i + 1}
          </span>
          <h3>{stage.title}</h3>
          <p>{stage.detail}</p>
          <code className="flow-type">{stage.type}</code>
        </li>
      ))}
    </ol>
  );
}
