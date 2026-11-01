-- v2 number 0238; ported from main 0209_login_code_template_snapshot (main 8ab819bc), see docs/main-v2-sync.md.
ALTER TABLE login_code_message_deliveries
    ADD COLUMN template text NOT NULL DEFAULT '';

COMMENT ON COLUMN login_code_message_deliveries.template IS
    'Rendered first-delivery template with {{code}} placeholder, never the secret code; empty means legacy built-in template';
