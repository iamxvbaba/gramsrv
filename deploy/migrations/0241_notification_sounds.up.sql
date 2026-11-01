-- v2 number 0241; ported from main 0212_notification_sounds (main e5971805), see docs/main-v2-sync.md.
ALTER TABLE account_reaction_settings
  ADD COLUMN sound jsonb NOT NULL DEFAULT '{"kind":0}'::jsonb;
ALTER TABLE notify_settings
  ADD COLUMN sound jsonb,
  ADD COLUMN stories_sound jsonb;
