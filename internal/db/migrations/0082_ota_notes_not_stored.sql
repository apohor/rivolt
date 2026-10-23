-- 0082_ota_notes_not_stored.sql: the OTA release-notes links are no longer stored.
--
-- Rivian's link is a pre-signed S3 URL valid for one hour that carries temporary credentials (X-Amz-Security-Token). It
-- was stored in vehicles.metadata.ota, refetched once a day, so it was expired when served and a credential at rest (in
-- exports and backups too). The app now asks for a fresh link when "Release notes" is tapped, and the stored record
-- keeps only has_notes. This removes the stored links, leaving has_notes where one existed.
UPDATE vehicles v
   SET metadata = jsonb_set(
         v.metadata, '{ota}',
         (v.metadata->'ota')
         || CASE WHEN v.metadata #>> '{ota,current,release_notes_url}' IS NOT NULL
                 THEN jsonb_build_object('current', ((v.metadata->'ota'->'current') - 'release_notes_url') || '{"has_notes":true}'::jsonb)
                 ELSE '{}'::jsonb END
         || CASE WHEN v.metadata #>> '{ota,available,release_notes_url}' IS NOT NULL
                 THEN jsonb_build_object('available', ((v.metadata->'ota'->'available') - 'release_notes_url') || '{"has_notes":true}'::jsonb)
                 ELSE '{}'::jsonb END)
 WHERE v.metadata #>> '{ota,current,release_notes_url}' IS NOT NULL
    OR v.metadata #>> '{ota,available,release_notes_url}' IS NOT NULL;
