-- Revoking an app session deletes the push devices registered with it, in the same
-- statement, whichever code path revoked it. Sends already stop at once (they only
-- reach live sessions); this also removes the stored Apple push address, as the
-- privacy page says. Rows of sessions that merely expire go with the session when
-- it is purged.
CREATE OR REPLACE FUNCTION sessions_revoke_push_devices() RETURNS trigger AS $$
BEGIN
	DELETE FROM push_apns_devices WHERE session_id = NEW.id;
	RETURN NULL;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS sessions_revoke_push_devices ON sessions;
CREATE TRIGGER sessions_revoke_push_devices
	AFTER UPDATE OF revoked_at ON sessions
	FOR EACH ROW
	WHEN (NEW.revoked_at IS NOT NULL AND OLD.revoked_at IS NULL)
	EXECUTE FUNCTION sessions_revoke_push_devices();
