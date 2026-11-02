-- Grok safety rejections are recorded as request_type=6. The previous check
-- constraint stopped at 5, so the fixed-fee usage row could not be inserted.
ALTER TABLE usage_logs
    DROP CONSTRAINT IF EXISTS usage_logs_request_type_check;

ALTER TABLE usage_logs
    ADD CONSTRAINT usage_logs_request_type_check
    CHECK (request_type IN (0, 1, 2, 3, 4, 5, 6)) NOT VALID;
