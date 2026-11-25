ALTER TABLE users
    ADD COLUMN locale text NOT NULL DEFAULT '' CHECK (locale = '' OR locale ~ '^[a-z]{2,3}(-[A-Za-z0-9]{2,8})*$');

ALTER TABLE organizations
    ADD COLUMN default_locale text NOT NULL DEFAULT '' CHECK (default_locale = '' OR default_locale ~ '^[a-z]{2,3}(-[A-Za-z0-9]{2,8})*$');
