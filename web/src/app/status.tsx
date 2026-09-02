const OK = new Set(["SUCCEEDED", "HEALTHY"]);
const BAD = new Set(["FAILED", "DEAD", "LOST"]);
const WARN = new Set(["RETRYING", "SUSPECT", "CANCELLED"]);

/**
 * Renders a status with a colour that means something consistent across the
 * whole dashboard: green finished well, red did not, amber is in between, grey
 * is neither — not started, or finished on purpose.
 *
 * The word is always rendered, never only the colour — a status a reader can
 * only get from a hue is a status colour-blind readers cannot get at all.
 *
 * STOPPED is deliberately grey rather than red. A worker that announced its
 * shutdown is not a failure, and colouring it like one would undo the reason
 * the state exists.
 */
export function Status({ value }: { value: string }) {
  const tone = OK.has(value)
    ? "status-ok"
    : BAD.has(value)
      ? "status-bad"
      : WARN.has(value)
        ? "status-warn"
        : "status-idle";
  return <span className={`status ${tone}`}>{value}</span>;
}
