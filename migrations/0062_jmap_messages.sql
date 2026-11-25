CREATE TABLE jmap_mailboxes (
    mailbox_id uuid NOT NULL REFERENCES mailboxes (id) ON DELETE CASCADE,
    jmap_id text NOT NULL CHECK (jmap_id ~ '^m\.[a-z2-7]{20,64}$'),
    folder text NOT NULL,
    PRIMARY KEY (mailbox_id, jmap_id),
    UNIQUE (mailbox_id, folder)
);

CREATE TABLE jmap_messages (
    mailbox_id uuid NOT NULL REFERENCES mailboxes (id) ON DELETE CASCADE,
    jmap_id text NOT NULL CHECK (jmap_id ~ '^e\.[a-z2-7]{20,64}$'),
    folder text NOT NULL,
    uid_validity bigint NOT NULL CHECK (uid_validity > 0),
    uid bigint NOT NULL CHECK (uid > 0),
    message_id text NOT NULL DEFAULT '',
    PRIMARY KEY (mailbox_id, jmap_id),
    UNIQUE (mailbox_id, folder, uid_validity, uid)
);

CREATE INDEX jmap_messages_message_id_idx ON jmap_messages (mailbox_id, message_id) WHERE message_id <> '';
