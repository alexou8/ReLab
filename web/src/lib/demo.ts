/**
 * The recording the dashboard serves when no control plane is configured.
 *
 * It is a real export of real runs — five of them, made by
 * scripts/record-demo.sh against real PostgreSQL, with the worker crashes
 * delivered by SIGKILL — and not a fixture written to look plausible. That
 * matters: the whole claim of this project is that a run's recorded history is
 * a faithful account of what happened, and a demo built from invented history
 * would be an argument against it.
 *
 * The file is the exact output of `relab export`, so what the pages read here
 * is byte-for-byte the shape the live API serves. Nothing in the dashboard gets
 * a second code path that could quietly diverge.
 */

import data from "@/demo/snapshot.json";
import type { Run, RunEvent, Stats, Task, Worker } from "./api";

export interface Snapshot {
  recorded_at: string;
  relab_version: string;
  note?: string;
  runs: { run: Run; tasks: Task[]; events: RunEvent[] }[];
  workers: Worker[];
  stats: Stats;
}

// The JSON is checked in and typed by hand at this one boundary. `relab export`
// writes the engine's own structures, and the mismatch that would matter — a
// field renamed in Go — shows up as a missing column on the page rather than as
// a type error either way, so a structural cast here buys nothing.
const loaded = data as unknown as Snapshot;

export function snapshot(): Snapshot {
  return loaded;
}
