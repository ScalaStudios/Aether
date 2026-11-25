ALTER TABLE users
    ADD COLUMN undo_send_seconds smallint NOT NULL DEFAULT 0 CHECK (undo_send_seconds IN (0, 5, 10, 20, 30));

ALTER TABLE scheduled_messages
    ADD COLUMN undo boolean NOT NULL DEFAULT false;
