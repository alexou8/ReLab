import Link from "next/link";
import { fetchRuns } from "@/lib/api";
import { duration, short, time } from "@/lib/format";
import { ErrorState } from "../error-state";
import { Status } from "../status";

export const dynamic = "force-dynamic";

export default async function Runs() {
  let runs;
  try {
    runs = await fetchRuns(100);
  } catch (error) {
    return <ErrorState error={error} />;
  }

  if (runs.length === 0) {
    return (
      <p className="empty">
        No runs recorded. Start one with{" "}
        <code>relab run examples/data-pipeline.yaml</code>.
      </p>
    );
  }

  return (
    <>
      <h2>Runs</h2>
      <div className="table-scroll">
        <table>
          <thead>
            <tr>
              <th>Status</th>
              <th>Workflow</th>
              <th>Run</th>
              <th>Scenario</th>
              <th>Seed</th>
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
                  <Link href={`/runs/${run.ID}`}>{short(run.ID, 12)}</Link>
                </td>
                <td>{run.ScenarioName || <span className="detail">—</span>}</td>
                <td className="mono">{run.Seed}</td>
                <td className="mono">{time(run.CreatedAt)}</td>
                <td className="mono">
                  {duration(run.CreatedAt, run.CompletedAt) ?? "running"}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </>
  );
}
