ALTER TABLE scheduled_messages ADD COLUMN sender_mode text NOT NULL DEFAULT 'send_as' CHECK (sender_mode IN ('send_as', 'send_on_behalf'));

ALTER TABLE scheduled_messages ALTER COLUMN sender_mode DROP DEFAULT;

UPDATE scheduled_messages SET error = 'This message was scheduled before an upgrade and cannot be sent. Cancel it to get the draft back, then schedule it again.' WHERE sender_user_id IS NULL AND error IS NULL;
