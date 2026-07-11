ALTER TABLE private_messages
    ADD COLUMN request_fingerprint bytea NOT NULL DEFAULT '\x',
    ADD COLUMN recipient_delivered boolean NOT NULL DEFAULT false;

-- 旧行没有保存原始请求指纹，保留空 fingerprint，使后续重放显式返回
-- RANDOM_ID_DUPLICATE，禁止从可能已编辑的消息投影猜测/修复原请求。
-- recipient_delivered 可由同一私聊事实表精确回填，用于区分「被 block 后本就
-- 不投递」与「声明已投递但 recipient box 丢失」两种状态。
UPDATE private_messages AS p
SET recipient_delivered = true
WHERE p.sender_user_id <> p.recipient_user_id
  AND EXISTS (
    SELECT 1
    FROM message_boxes AS b
    WHERE b.private_message_id = p.id
      AND b.owner_user_id = p.recipient_user_id
  );

-- Keep these defaults after the expand step.  A pre-0062 process does not name
-- either column in INSERT, so dropping them while that process can still serve
-- traffic turns an otherwise compatible rolling deployment into a NOT NULL
-- failure.  The empty fingerprint is an explicit "unknown legacy receipt"
-- sentinel: the new replay path rejects it as RANDOM_ID_DUPLICATE before it can
-- interpret recipient_delivered.  It must never be reconstructed from mutable
-- message_boxes.
