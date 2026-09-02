-- 002_worker_stopped.sql: distinguish a worker that left from one that died.
--
-- Until now a worker that shut down cleanly simply stopped heartbeating, and
-- the reaper marked it LOST five beats later — the same state a SIGKILL
-- produces. That made every ordinary redeploy read as a crash in `relab
-- workers` and in the dashboard, and it cost the one distinction an operator
-- most wants from that table: was this expected?
--
-- STOPPED is a reporting state only. It is set by the process itself as it
-- exits, and nothing about lease reclamation changes: a lease is still released
-- by expiry, which is the mechanism that has to work when the process gets no
-- chance to say anything at all.

-- 001 declared the check inline, so its name came from PostgreSQL rather than
-- from us. It is dropped by looking it up instead of by guessing the generated
-- name, and the replacement is named, so a later migration has something stable
-- to alter.
DO $$
DECLARE constraint_name text;
BEGIN
    SELECT conname INTO constraint_name
    FROM pg_constraint
    WHERE conrelid = 'workers'::regclass
      AND contype = 'c'
      AND pg_get_constraintdef(oid) LIKE '%status%';
    IF constraint_name IS NULL THEN
        RAISE EXCEPTION 'workers has no status check constraint to replace';
    END IF;
    EXECUTE format('ALTER TABLE workers DROP CONSTRAINT %I', constraint_name);
END $$;

ALTER TABLE workers ADD CONSTRAINT workers_status_check
    CHECK (status IN ('HEALTHY', 'SUSPECT', 'LOST', 'STOPPED'));

-- A stopped worker is as terminal as a lost one and is likewise never
-- re-examined by the liveness sweep, so it leaves the index too.
DROP INDEX workers_liveness_idx;
CREATE INDEX workers_liveness_idx ON workers (last_heartbeat)
    WHERE status NOT IN ('LOST', 'STOPPED');
