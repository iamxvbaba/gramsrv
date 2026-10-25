-- v2 编号 0241；移植自 main 0212_notification_sounds（main e5971805），见 docs/main-v2-sync.md。
ALTER TABLE account_reaction_settings
  ADD COLUMN sound jsonb NOT NULL DEFAULT '{"kind":0}'::jsonb;
ALTER TABLE notify_settings
  ADD COLUMN sound jsonb,
  ADD COLUMN stories_sound jsonb;
