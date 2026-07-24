-- contacts.addContact historically replaced an omitted phone with users.phone.
-- Those rows are indistinguishable from a client-supplied copy of the same
-- number, so privacy-safe repair must treat every exact account-phone copy as
-- ambiguous. The contact relationship and owner-scoped names/notes remain; a
-- later contacts.importContacts sync can explicitly restore a known phone.
--
-- This is a one-time write-path repair. Runtime reads must not normalize or
-- second-guess the bad shape.
UPDATE contacts AS c
SET contact_phone = '',
    updated_at = now()
FROM users AS u
WHERE u.id = c.contact_user_id
  AND c.contact_phone <> ''
  AND c.contact_phone = u.phone;
