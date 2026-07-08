ALTER TABLE auth_keys
  ADD COLUMN layer integer NOT NULL DEFAULT 0,
  ADD COLUMN device_model varchar(128) NOT NULL DEFAULT '',
  ADD COLUMN platform varchar(64) NOT NULL DEFAULT '',
  ADD COLUMN system_version varchar(64) NOT NULL DEFAULT '',
  ADD COLUMN api_id integer NOT NULL DEFAULT 0,
  ADD COLUMN app_version varchar(64) NOT NULL DEFAULT '';
