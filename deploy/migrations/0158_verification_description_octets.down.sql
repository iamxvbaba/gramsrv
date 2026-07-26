-- Narrowing back can only succeed once every stored description fits the old
-- octet bound, so truncate the ones that do not: a CHECK cannot be added while a
-- stored row violates it. left() counts characters, so take a conservative quarter
-- of the octet bound -- every rune is at most four bytes.
UPDATE public.bot_verifier_settings
SET default_description = left(default_description, 256)
WHERE octet_length(default_description) > 1024;

UPDATE public.custom_verifications
SET description = left(description, 256)
WHERE octet_length(description) > 1024;

UPDATE public.custom_verification_requests
SET requested_description = left(requested_description, 256)
WHERE octet_length(requested_description) > 1024;

ALTER TABLE public.bot_verifier_settings
    DROP CONSTRAINT IF EXISTS bot_verifier_settings_default_description_check;
ALTER TABLE public.bot_verifier_settings
    ADD CONSTRAINT bot_verifier_settings_default_description_check
    CHECK (octet_length(default_description) <= 1024);

ALTER TABLE public.custom_verifications
    DROP CONSTRAINT IF EXISTS custom_verifications_description_check;
ALTER TABLE public.custom_verifications
    ADD CONSTRAINT custom_verifications_description_check
    CHECK (octet_length(description) <= 1024);

ALTER TABLE public.custom_verification_requests
    DROP CONSTRAINT IF EXISTS custom_verification_requests_requested_description_check;
ALTER TABLE public.custom_verification_requests
    ADD CONSTRAINT custom_verification_requests_requested_description_check
    CHECK (octet_length(requested_description) <= 1024);
