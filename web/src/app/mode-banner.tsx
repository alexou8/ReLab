import { apiBase, mode } from "@/lib/api";
import { snapshot } from "@/lib/demo";

/**
 * States, once and at the bottom of every page, where the data above it came
 * from.
 *
 * A dashboard showing recorded data without saying so is worse than one showing
 * nothing: a reader has no way to tell. So the recording announces itself, and
 * says when it was made and what made it.
 *
 * It sits in a status line rather than a banner because that is where a person
 * who lives in a terminal looks to find out which session they are in. It is
 * always on screen and never in the way of the first thing on the page.
 */
export function StatusLine() {
  if (mode() === "live") {
    return (
      <div className="statusline mode-live" role="status">
        <span className="mode-label">LIVE</span>
        <span>
          reading <code>{apiBase()}</code>
        </span>
        <span className="sep" aria-hidden="true">
          |
        </span>
        <span>every page reflects the database at load</span>
      </div>
    );
  }

  const { recorded_at, relab_version } = snapshot();
  return (
    <div className="statusline mode-demo" role="status">
      <span className="mode-label">RECORDING</span>
      <span>
        five real runs exported from real PostgreSQL by{" "}
        <code>scripts/record-demo.sh</code>, crashes delivered by{" "}
        <code>SIGKILL</code>
      </span>
      <span className="sep" aria-hidden="true">
        |
      </span>
      <span>
        relab {relab_version}, recorded{" "}
        <time dateTime={recorded_at}>{recorded_at.slice(0, 10)}</time>
      </span>
    </div>
  );
}
