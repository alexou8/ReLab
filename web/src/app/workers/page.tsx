import { fetchWorkers } from "@/lib/api";
import { short, time } from "@/lib/format";
import { ErrorState } from "../error-state";
import { Status } from "../status";

export const dynamic = "force-dynamic";

export default async function Workers() {
  let workers;
  try {
    workers = await fetchWorkers();
  } catch (error) {
    return <ErrorState error={error} />;
  }

  if (workers.length === 0) {
    return (
      <>
        <h2>Workers</h2>
        <p className="empty">
          No workers registered. Start one with <code>relab worker</code>.
        </p>
      </>
    );
  }

  const lost = workers.filter((w) => w.Status === "LOST").length;

  return (
    <>
      <h2>Workers</h2>
      <p className="detail note">
        A worker becomes SUSPECT after three missed heartbeats and LOST after
        five, at which point its leases are released. One missed heartbeat never
        means failure. STOPPED is different: the process said it was leaving, so
        an ordinary shutdown does not read as a crash.{" "}
        {lost > 0
          ? `${lost} of these ${lost === 1 ? "was" : "were"} never heard from again.`
          : "None of these went away without saying so."}
      </p>
      <div className="table-scroll">
        <table>
          <caption className="sr-only">
            Every registered worker, most recently seen first.
          </caption>
          <thead>
            <tr>
              <th scope="col">Status</th>
              <th scope="col">Worker</th>
              <th scope="col">Host</th>
              <th scope="col">Version</th>
              <th scope="col" className="mono">
                Load
              </th>
              <th scope="col" className="mono">
                Last heartbeat
              </th>
            </tr>
          </thead>
          <tbody>
            {workers.map((worker) => (
              <tr key={worker.ID}>
                <td>
                  <Status value={worker.Status} />
                </td>
                <th scope="row" className="row-name mono">
                  {short(worker.ID, 12)}
                </th>
                <td>{worker.Hostname}</td>
                <td className="mono">{worker.Version}</td>
                <td className="mono">
                  {worker.ActiveTasks}/{worker.Capacity}
                </td>
                <td className="mono">{time(worker.LastHeartbeat)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </>
  );
}
