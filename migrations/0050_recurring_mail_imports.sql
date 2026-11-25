CREATE TABLE mail_import_schedules (
    id uuid PRIMARY KEY,
    mailbox_id uuid NOT NULL REFERENCES mailboxes (id) ON DELETE CASCADE,
    user_id uuid REFERENCES users (id) ON DELETE CASCADE,
    host text NOT NULL,
    port integer NOT NULL,
    security text NOT NULL,
    username text NOT NULL,
    credential bytea,
    interval_minutes integer NOT NULL CHECK (interval_minutes BETWEEN 1 AND 10080),
    ends_at timestamptz,
    next_run_at timestamptz NOT NULL DEFAULT now(),
    sync_flags boolean NOT NULL DEFAULT false,
    sync_deletions boolean NOT NULL DEFAULT false,
    status text NOT NULL DEFAULT 'scheduled',
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX mail_import_schedules_active_mailbox ON mail_import_schedules (mailbox_id) WHERE credential IS NOT NULL;
CREATE INDEX mail_import_schedules_due ON mail_import_schedules (next_run_at) WHERE credential IS NOT NULL;

ALTER TABLE mail_imports ALTER COLUMN user_id DROP NOT NULL;
ALTER TABLE mail_imports ADD COLUMN schedule_id uuid REFERENCES mail_import_schedules (id) ON DELETE SET NULL;
CREATE INDEX mail_imports_schedule ON mail_imports (schedule_id, created_at DESC);

CREATE TABLE mail_import_copies (
    schedule_id uuid NOT NULL REFERENCES mail_import_schedules (id) ON DELETE CASCADE,
    source_folder text NOT NULL,
    source_validity bigint NOT NULL,
    source_uid bigint NOT NULL,
    target_folder text NOT NULL,
    target_validity bigint NOT NULL,
    target_uid bigint NOT NULL,
    PRIMARY KEY (schedule_id, source_folder, source_validity, source_uid, target_folder, target_validity)
);
