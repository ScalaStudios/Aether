CREATE TABLE user_avatars (
    user_id uuid PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    image bytea NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);
