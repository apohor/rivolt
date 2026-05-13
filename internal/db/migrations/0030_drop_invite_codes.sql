-- 0030_drop_invite_codes.sql
--
-- Magic-link signup tokens (0029) replace the manual invite-code
-- copy/paste flow. No pending or in-flight invite codes remained
-- when this migration was written, so we drop the table outright
-- rather than carry a deprecated code path forever.
--
-- signup_requests.invite_code was a FK to invite_codes(code) for
-- approvals minted before the token flow; both go in the same
-- migration so we don't leave a dangling FK target. The historical
-- "this request was approved with code X" linkage is lost, but the
-- decided_at + decided_by columns still record who approved when.

ALTER TABLE signup_requests
    DROP COLUMN IF EXISTS invite_code;

DROP TABLE IF EXISTS invite_codes;
