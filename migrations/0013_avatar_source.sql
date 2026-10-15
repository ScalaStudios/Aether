ALTER TABLE user_avatars ADD COLUMN source text NOT NULL DEFAULT 'upload' CHECK (source IN ('upload', 'provider'));
