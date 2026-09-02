import { ApiError } from "@/lib/api";

/**
 * The one place an API failure is rendered.
 *
 * It names the endpoint and the likely fix rather than saying "something went
 * wrong": the overwhelmingly common cause is that the control plane is not
 * running, and a page that says so saves a reader from debugging the dashboard
 * when the problem is elsewhere.
 */
export function ErrorState({ error }: { error: unknown }) {
  const endpoint = error instanceof ApiError ? error.endpoint : "the API";
  const reason =
    error instanceof Error ? error.message : "an unexpected failure";

  return (
    <div className="notice" role="alert">
      <h3>Could not load {endpoint}</h3>
      <p>
        {reason}. The dashboard reads from the ReLab control plane; start it
        with <code>relab server</code>, or point the dashboard elsewhere with{" "}
        <code>RELAB_API_URL</code>.
      </p>
    </div>
  );
}
