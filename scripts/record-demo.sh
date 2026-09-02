#!/usr/bin/env bash
# Records the dashboard's demo snapshot by actually running the scenarios.
#
# The snapshot the deployed dashboard serves is a recording of real runs, not a
# fixture someone wrote by hand, and this script is how it is made. Running it
# again against the same PostgreSQL reproduces an equivalent recording, which is
# the only way the claim "this is what happened" stays checkable.
#
#   RELAB_DSN=postgres://... ./scripts/record-demo.sh
#
# It creates runs in the database it is pointed at. Point it at a scratch one.

set -euo pipefail
cd "$(dirname "$0")/.."

OUT=${OUT:-web/src/demo/snapshot.json}

# The compressed timings the compose stack uses. Recovery in the recording then
# takes seconds rather than the 30s a production lease would, which is what
# makes the timeline readable on one screen. docs/reliability.md gives the
# production defaults and what they cost.
export RELAB_LEASE_DURATION=${RELAB_LEASE_DURATION:-2s}
export RELAB_LEASE_RENEW_INTERVAL=${RELAB_LEASE_RENEW_INTERVAL:-500ms}
export RELAB_HEARTBEAT_INTERVAL=${RELAB_HEARTBEAT_INTERVAL:-300ms}
export RELAB_REAPER_INTERVAL=${RELAB_REAPER_INTERVAL:-200ms}
export RELAB_LOG_LEVEL=${RELAB_LOG_LEVEL:-error}

go build -o bin/relab ./cmd/relab
bin/relab migrate

# Oldest first: the run list is newest first, so the headline scenario is
# recorded last and lands at the top.
echo "recording: clean baseline"
bin/relab run examples/data-pipeline.yaml >/dev/null

echo "recording: upstream never recovers, run dead-letters"
bin/relab run examples/data-pipeline.yaml \
  --scenario examples/scenarios/retry-exhaustion.yaml >/dev/null || true

echo "recording: duplicate delivery, effect suppressed"
bin/relab test examples/effectful.yaml \
  --scenario examples/scenarios/duplicate-delivery.yaml >/dev/null

echo "recording: worker killed mid-task, task recovered"
bin/relab test examples/data-pipeline.yaml \
  --scenario examples/scenarios/worker-crash.yaml >/dev/null

echo "recording: worker killed after the side effect, effect not repeated"
bin/relab test examples/effectful.yaml \
  --scenario examples/scenarios/worker-crash-effectful.yaml >/dev/null

mkdir -p "$(dirname "$OUT")"
bin/relab export --limit 10 \
  --note "Recorded by scripts/record-demo.sh against real PostgreSQL and real worker processes. The worker crashes are SIGKILL, not simulated." \
  > "$OUT"
echo "wrote $OUT"
