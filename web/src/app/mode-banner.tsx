import { apiBase, mode } from "@/lib/api";
import { snapshot } from "@/lib/demo";

/**
 * States, once and at the top of every page, where the data below it came from.
 *
 * A dashboard showing recorded data without saying so is worse than one showing
 * nothing: a reader has no way to tell. So the recording announces itself, and
 * says when it was made and what made it.
 */
export function ModeBanner() {
  if (mode() === "live") {
    return (
      <p className="mode mode-live">
        <span className="mode-label">Live</span>
        Reading from the ReLab control plane at <code>{apiBase()}</code>.
      </p>
    );
  }

  const { recorded_at, relab_version } = snapshot();
  return (
    <p className="mode mode-demo">
      <span className="mode-label">Recording</span>
      No control plane is configured, so these pages serve a recorded export of
      five real runs — made by <code>scripts/record-demo.sh</code> against real
      PostgreSQL, with the worker crashes delivered by <code>SIGKILL</code>.
      Nothing here is simulated and nothing is live.{" "}
      <span className="detail">
        relab {relab_version}, recorded{" "}
        <time dateTime={recorded_at}>{recorded_at.slice(0, 10)}</time>
      </span>
    </p>
  );
}
