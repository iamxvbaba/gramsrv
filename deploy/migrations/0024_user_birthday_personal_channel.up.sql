-- account.updateBirthday / account.updatePersonalChannel: the "birthday" and
-- "personal channel" in a profile. The birthday is stored as month/day/year (all
-- zero = unset; year=0 = only month/day, no year); the personal channel stores the
-- target channel id (0 = unset/cleared), and the profile projection resolves the
-- channel object and its latest post id by that id.
ALTER TABLE public.users
  ADD COLUMN birthday_day integer DEFAULT 0 NOT NULL,
  ADD COLUMN birthday_month integer DEFAULT 0 NOT NULL,
  ADD COLUMN birthday_year integer DEFAULT 0 NOT NULL,
  ADD COLUMN personal_channel_id bigint DEFAULT 0 NOT NULL;
