-- Emoji in third-party verification descriptions.
--
-- domain.MaxCustomVerificationDescriptionLength is 1024 and is enforced with
-- utf8.RuneCountInString, so a description may legitimately be 1024 *characters*.
-- These three CHECKs bound the same field at 1024 *octets*, which is the same
-- number only for ASCII: one emoji is four bytes and one Cyrillic letter is two, so
-- a description well inside the documented limit is rejected by the database with an
-- opaque constraint error instead of a domain error -- and the operator sees a
-- failure whose message says nothing about length.
--
-- The official verification side already got this right:
-- verification_applications_description_check bounds octet_length at 4096 for the
-- same 1024-rune domain limit. Bring the third-party tables to the same rule, which
-- is the worst case of 1024 four-byte runes.
--
-- Widening a CHECK can never invalidate a stored row, so no data migration is
-- needed.

ALTER TABLE public.bot_verifier_settings
    DROP CONSTRAINT IF EXISTS bot_verifier_settings_default_description_check;
ALTER TABLE public.bot_verifier_settings
    ADD CONSTRAINT bot_verifier_settings_default_description_check
    CHECK (octet_length(default_description) <= 4096);

ALTER TABLE public.custom_verifications
    DROP CONSTRAINT IF EXISTS custom_verifications_description_check;
ALTER TABLE public.custom_verifications
    ADD CONSTRAINT custom_verifications_description_check
    CHECK (octet_length(description) <= 4096);

ALTER TABLE public.custom_verification_requests
    DROP CONSTRAINT IF EXISTS custom_verification_requests_requested_description_check;
ALTER TABLE public.custom_verification_requests
    ADD CONSTRAINT custom_verification_requests_requested_description_check
    CHECK (octet_length(requested_description) <= 4096);
