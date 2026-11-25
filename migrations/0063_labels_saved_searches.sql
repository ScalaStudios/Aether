CREATE TABLE mailbox_labels (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    mailbox_id uuid NOT NULL REFERENCES mailboxes (id) ON DELETE CASCADE,
    name text NOT NULL,
    color text NOT NULL,
    keyword text NOT NULL,
    position integer NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT mailbox_labels_name_len CHECK (char_length(btrim(name)) BETWEEN 1 AND 40),
    CONSTRAINT mailbox_labels_color CHECK (color IN ('red', 'orange', 'yellow', 'green', 'blue', 'purple', 'gray', 'pink')),
    CONSTRAINT mailbox_labels_keyword CHECK (keyword ~ '^[a-z][a-z0-9_]{0,62}$'),
    CONSTRAINT mailbox_labels_position CHECK (position >= 0)
);

CREATE UNIQUE INDEX mailbox_labels_name_idx ON mailbox_labels (mailbox_id, lower(name));
CREATE UNIQUE INDEX mailbox_labels_keyword_idx ON mailbox_labels (mailbox_id, keyword);

CREATE TABLE saved_searches (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    mailbox_id uuid NOT NULL REFERENCES mailboxes (id) ON DELETE CASCADE,
    name text NOT NULL,
    query text NOT NULL DEFAULT '',
    unread boolean NOT NULL DEFAULT false,
    flagged boolean NOT NULL DEFAULT false,
    has_attachments boolean NOT NULL DEFAULT false,
    from_text text NOT NULL DEFAULT '',
    to_text text NOT NULL DEFAULT '',
    since_date date,
    until_date date,
    label_id uuid REFERENCES mailbox_labels (id) ON DELETE SET NULL,
    folder text NOT NULL DEFAULT '',
    all_folders boolean NOT NULL DEFAULT false,
    position integer NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT saved_searches_name_len CHECK (char_length(btrim(name)) BETWEEN 1 AND 80),
    CONSTRAINT saved_searches_query_len CHECK (char_length(query) <= 500),
    CONSTRAINT saved_searches_from_len CHECK (char_length(from_text) <= 200),
    CONSTRAINT saved_searches_to_len CHECK (char_length(to_text) <= 200),
    CONSTRAINT saved_searches_folder_len CHECK (char_length(folder) <= 400),
    CONSTRAINT saved_searches_scope CHECK ((all_folders AND folder = '') OR (NOT all_folders AND folder <> '')),
    CONSTRAINT saved_searches_position CHECK (position >= 0)
);

CREATE UNIQUE INDEX saved_searches_name_idx ON saved_searches (mailbox_id, lower(name));
