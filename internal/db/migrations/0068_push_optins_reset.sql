-- 0068_push_optins_reset.sql — turn every push opt-in switch off, once.
--
-- 0067 reset on_plug_in_reminder, but the pre-U1 Settings page (and any old pod
-- during a rolling deploy) could still post on_plug_in_reminder:true afterwards.
-- This runs again at boot of the release that also ships the new Settings
-- screen and the server-side change that makes POST /api/push/subscribe ignore
-- both switches, so from here a switch is on only after a deliberate tap in the
-- new screen (POST /api/push/optin). The plug-in reminder and milestones are
-- opt-in only.
--
-- Rollback: none. The previous values are not kept and cannot be restored (no
-- account chose them: nothing sent either message before this).
UPDATE push_subscriptions
   SET on_plug_in_reminder = FALSE, on_milestones = FALSE
 WHERE on_plug_in_reminder OR on_milestones;
