import Link from "next/link";
import { fetchRuns, type Run } from "@/lib/api";
import { duration, short, time } from "@/lib/format";
import { ErrorState } from "../error-state";
import { Status } from "../status";

export const dynamic = "force-dynamic";

export default async function Runs({
  searchParams,
}: {
  searchParams: Promise<{ status?: string; workflow?: string }>;
}) {
  const { status, workflow } = await searchParams;

  let runs;
  try {
    runs = await fetchRuns(100);
  } catch (error) {
    return <ErrorState error={error} />;
  }

  if (runs.length === 0) {
    return (
      <>
        <h2>Runs</h2>
        <p className="empty">
          No runs recorded. Start one with{" "}
          <code>relab run examples/data-pipeline.yaml</code>, or a failing one
          with{" "}
          <code>
            relab test examples/data-pipeline.yaml --scenario
            examples/scenarios/worker-crash.yaml
          </code>
          .
        </p>
      </>
    );
  }

  // Filtering here rather than in the API: the list is capped at 100 rows, and
  // a round trip to narrow a hundred rows already in memory would be slower and
  // would make the counts on the filter links impossible to show.
  const shown = runs.filter(
    (r) =>
      (!status || r.Status === status) &&
      (!workflow || r.WorkflowName === workflow),
  );

  return (
    <>
      <h2>Runs</h2>
      <FilterBar runs={runs} status={status} workflow={workflow} />

      {shown.length === 0 ? (
        <p className="empty">
          No run matches that filter. <Link href="/runs">Clear it</Link> to see
          all {runs.length}.
        </p>
      ) : (
        <div className="table-scroll">
          <table>
            <caption className="sr-only">
              {shown.length} of {runs.length} runs, newest first.
            </caption>
            <thead>
              <tr>
                <th scope="col">Status</th>
                <th scope="col">Workflow</th>
                <th scope="col">Run</th>
                <th scope="col">Scenario</th>
                <th scope="col" className="mono">
                  Seed
                </th>
                <th scope="col" className="mono">
                  Started
                </th>
                <th scope="col" className="mono">
                  Duration
                </th>
              </tr>
            </thead>
            <tbody>
              {shown.map((run) => (
                <tr key={run.ID}>
                  <td>
                    <Status value={run.Status} />
                  </td>
                  <th scope="row" className="row-name">
                    {run.WorkflowName}{" "}
                    <span className="detail">v{run.WorkflowVer}</span>
                  </th>
                  <td className="mono">
                    <Link href={`/runs/${run.ID}`}>{short(run.ID, 12)}</Link>
                  </td>
                  <td>{run.ScenarioName || <span className="detail">·</span>}</td>
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
      )}
    </>
  );
}

/**
 * The filters are links carrying counts, so the reader can see how many runs
 * failed before deciding to look at them, and so a filtered list is a URL
 * someone can be sent.
 */
function FilterBar({
  runs,
  status,
  workflow,
}: {
  runs: Run[];
  status?: string;
  workflow?: string;
}) {
  const statuses = tally(runs.map((r) => r.Status));
  const workflows = tally(runs.map((r) => r.WorkflowName));

  return (
    <>
      <nav className="filters" aria-label="Filter runs by status">
        <Link
          href="/runs"
          className={!status && !workflow ? "filter filter-on" : "filter"}
          aria-current={!status && !workflow ? "true" : undefined}
        >
          All <span className="filter-count">{runs.length}</span>
        </Link>
        {statuses.map(([value, count]) => (
          <Link
            key={value}
            href={`/runs?status=${value}`}
            className={status === value ? "filter filter-on" : "filter"}
            aria-current={status === value ? "true" : undefined}
          >
            {value} <span className="filter-count">{count}</span>
          </Link>
        ))}
      </nav>
      {workflows.length > 1 ? (
        <nav className="filters" aria-label="Filter runs by workflow">
          {workflows.map(([value, count]) => (
            <Link
              key={value}
              href={`/runs?workflow=${encodeURIComponent(value)}`}
              className={workflow === value ? "filter filter-on" : "filter"}
              aria-current={workflow === value ? "true" : undefined}
            >
              {value} <span className="filter-count">{count}</span>
            </Link>
          ))}
        </nav>
      ) : null}
    </>
  );
}

function tally(values: string[]): [string, number][] {
  const counts = new Map<string, number>();
  for (const v of values) counts.set(v, (counts.get(v) ?? 0) + 1);
  return [...counts.entries()].sort(([a], [b]) => a.localeCompare(b));
}
