UPDATE channel_members m
SET admin_rights = m.admin_rights || '{"ManageRanks": true}'::jsonb
FROM channels c
WHERE c.id = m.channel_id
  AND NOT c.deleted
  AND c.megagroup
  AND NOT c.broadcast
  AND m.status = 'active'
  AND (
    m.role = 'creator'
    OR (
      m.role = 'admin'
      AND COALESCE((m.admin_rights ->> 'ChangeInfo')::boolean, false)
      AND COALESCE((m.admin_rights ->> 'DeleteMessages')::boolean, false)
      AND COALESCE((m.admin_rights ->> 'BanUsers')::boolean, false)
      AND COALESCE((m.admin_rights ->> 'InviteUsers')::boolean, false)
      AND COALESCE((m.admin_rights ->> 'PinMessages')::boolean, false)
      AND COALESCE((m.admin_rights ->> 'AddAdmins')::boolean, false)
      AND COALESCE((m.admin_rights ->> 'ManageCall')::boolean, false)
    )
  );
