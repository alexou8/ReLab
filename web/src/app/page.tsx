import Link from "next/link";
import { fetchRuns, fetchStats, fetchWorkers } from "@/lib/api";
import { duration, short, time } from "@/lib/format";
import { ErrorState } from "./error-state";
import { Status } from "./status";

// The overview is the page someone leaves open while killing a worker, so it
// must reflect the database on every load rather than a cached render.
export const dynamic = "force-dynamic";

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
  const live = workers.filter((w) => w.Status !== "LOST").length;
  const running = stats.runs_by_status["RUNNING"] ?? 0;

  return (
    <>
      <h2>Now</h2>
      <div className="counts">
        <Count label="Running" value={running} />
        <Count label="Queue depth" value={stats.queue_depth} />
        <Count label="Live workers" value={live} />
        <Count label="Dead letters" value={stats.dead_letters} />
      </div>

      <h2>Runs by status</h2>
      {Object.keys(stats.runs_by_status).length === 0 ? (
        <p className="empty">
          No runs yet. Start one with{" "}
          <code>relab run examples/data-pipeline.yaml</code>.
        </p>
      ) : (
        <div className="counts">
          {Object.entries(stats.runs_by_status)
            .sort(([a], [b]) => a.localeCompare(b))
            .map(([status, count]) => (
              <Count key={status} label={status} value={count} />
            ))}
        </div>
      )}

      <h2>Latest runs</h2>
      {runs.length === 0 ? (
        <p className="empty">Nothing has run yet.</p>
      ) : (
        <div className="table-scroll">
          <table>
            <thead>
              <tr>
                <th>Status</th>
                <th>Workflow</th>
                <th>Run</th>
                <th>Started</th>
                <th>Duration</th>
              </tr>
            </thead>
            <tbody>
              {runs.map((run) => (
                <tr key={run.ID}>
                  <td>
                    <Status value={run.Status} />
                  </td>
                  <td>
                    {run.WorkflowName} <span className="detail">v{run.WorkflowVer}</span>
                  </td>
                  <td className="mono">
                    <Link href={`/runs/${run.ID}`}>{short(run.ID)}</Link>
                  </td>
                  <td className="mono">{time(run.CreatedAt)}</td>
                  <td className="mono">
                    {duration(run.CreatedAt, run.CompletedAt) ?? "—"}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </>
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
