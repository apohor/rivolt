package main

import "github.com/apohor/rivolt/internal/backfill"

// backfillJobs are the background backfills the server runs (internal/backfill, zero-impact deploys G6b). Data that has to move after a schema
// change goes here, not into the migration: a job is a named, idempotent, key-ordered step that the runner calls in short transactions on one pod
// at a time, resumable from its stored cursor. Never rename a job that has started (its name keys its progress). Readers of what a job fills treat
// the old shape as valid until the job is done (rivolt_backfill_done{job}), and a "-- contract:" removal of the old shape waits for that.
// A job is registered by its own PR with the devils-advocate's ack and a row-count test on a fixture; its new value is computed inside the UPDATE
// from the row's own columns, and on the recorder's tables it writes only new or unfilled columns (see internal/backfill). Pause one job without a
// deploy: INSERT INTO flags (name, value, updated_by) VALUES ('backfill.<job>.paused', '{"paused": true}', 'lead') ON CONFLICT (name) DO UPDATE SET value = EXCLUDED.value.
// Empty today: the existing backfills ran inside their migrations.
var backfillJobs = []backfill.Job{}
