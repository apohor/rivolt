package main

import "github.com/apohor/rivolt/internal/backfill"

// backfillJobs are the background backfills the server runs (internal/backfill, zero-impact deploys G6b). Data that has to move after a schema
// change goes here, not into the migration: a job is a named, idempotent, key-ordered step that the runner calls in short transactions on one pod
// at a time, resumable from its stored cursor. Never rename a job that has started (its name keys its progress). Readers of what a job fills treat
// the old shape as valid until the job is done (rivolt_backfill_done{job}), and a "-- contract:" removal of the old shape waits for that.
// Empty today: the existing backfills ran inside their migrations.
var backfillJobs = []backfill.Job{}
