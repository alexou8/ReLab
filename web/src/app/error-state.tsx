import { ApiError, apiBase } from "@/lib/api";

/**
 * The one place an API failure is rendered.
 *
 * It names the endpoint and the likely fix rather than saying "something went
 * wrong": the overwhelmingly common cause is that the control plane is not
 * running, and a page that says so saves a reader from debugging the dashboard
 * when the problem is elsewhere.
 *
 * It never falls back to the recorded demo data. A dashboard that quietly
 * showed last week's runs during an incident would be worse than one that shows
 * nothing at all.
 */
export function ErrorState({ error }: { error: unknown }) {
  const endpoint = error instanceof ApiError ? error.endpoint : "the API";
  const reason =
    error instanceof Error ? error.message : "an unexpected failure";

  return (
    <div className="notice notice-bad" role="alert">
      <h3>The ReLab control plane could not be reached</h3>
      <p>
        <code>{endpoint}</code>: {reason}.
      </p>
      <p>
        This dashboard is configured to read from <code>{apiBase()}</code>.
        Start a control plane with <code>relab server</code>, point{" "}
        <code>RELAB_API_URL</code> somewhere else, or unset it to serve the
        recorded demo instead.
      </p>
      <p>
        Nothing is shown in place of the missing data on purpose. Recorded runs
        rendered as though they were live would be worse than an empty page.
      </p>
    </div>
  );
}
