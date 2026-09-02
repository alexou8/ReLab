/** Presentation helpers shared by the pages. */

/**
 * Formats a duration between two timestamps. Returns null when the run has not
 * finished, so a caller renders "still running" rather than a wrong number.
 */
export function duration(from: string, to: string | null): string | null {
  if (!to) return null;
  const ms = new Date(to).getTime() - new Date(from).getTime();
  if (!Number.isFinite(ms) || ms < 0) return null;
  if (ms < 1000) return `${ms}ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(2)}s`;
  const minutes = Math.floor(ms / 60_000);
  const seconds = Math.round((ms % 60_000) / 1000);
  return `${minutes}m ${seconds}s`;
}

/** Formats a timestamp for a table cell. */
export function time(value: string | null): string {
  if (!value) return "—";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "—";
  return date.toLocaleTimeString([], {
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  });
}

/** Formats a timestamp with milliseconds, for the event timeline. */
export function preciseTime(value: string): string {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "—";
  const ms = String(date.getMilliseconds()).padStart(3, "0");
  return `${date.toLocaleTimeString([], { hour12: false })}.${ms}`;
}

/** Shortens a hash or id for display without losing its usefulness. */
export function short(id: string, length = 8): string {
  return id.length > length ? id.slice(0, length) : id;
}

/**
 * Picks out the fields of an event payload worth showing on one line. Reading
 * the payload generically rather than switching on the event type means a new
 * event type renders something useful without this file changing.
 */
export function summarisePayload(payload: Record<string, unknown>): string {
  const interesting = [
    "attempt",
    "next_attempt",
    "error",
    "reason",
    "detail",
    "delay_ms",
    "duration_ms",
    "fault_type",
    "idempotency_key",
    "task_count",
    "tasks_succeeded",
    "leases_released",
    "missed_beats",
  ];
  const parts: string[] = [];
  for (const key of interesting) {
    const value = payload[key];
    if (value === undefined || value === null || value === "") continue;
    const text = String(value);
    parts.push(`${key}=${text.length > 60 ? `${text.slice(0, 57)}…` : text}`);
  }
  return parts.join("  ");
}
