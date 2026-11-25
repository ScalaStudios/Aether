ALTER TABLE organizations
    ADD COLUMN deleted_item_retention_days integer NOT NULL DEFAULT 14 CHECK (deleted_item_retention_days BETWEEN 1 AND 30);
