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
      <p className="empty">
        No workers registered. Start one with <code>relab worker</code>.
      </p>
    );
  }

  return (
    <>
      <h2>Workers</h2>
      <p className="detail" style={{ marginBottom: 12 }}>
        A worker becomes SUSPECT after three missed heartbeats and LOST after
        five, at which point its leases are released. One missed heartbeat never
        means failure.
      </p>
      <div className="table-scroll">
        <table>
          <thead>
            <tr>
              <th>Status</th>
              <th>Worker</th>
              <th>Host</th>
              <th>Version</th>
              <th>Load</th>
              <th>Last heartbeat</th>
            </tr>
          </thead>
          <tbody>
            {workers.map((worker) => (
              <tr key={worker.ID}>
                <td>
                  <Status value={worker.Status} />
                </td>
                <td className="mono">{short(worker.ID, 12)}</td>
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
