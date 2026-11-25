CREATE TABLE organization_disabled_features (
    org_id uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    feature text NOT NULL CHECK (feature IN ('calendar', 'dav', 'mailImport', 'appPasswords', 'externalForwarding', 'personalAliases', 'webhooks', 'apiKeys')),
    disabled_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, feature)
);

CREATE OR REPLACE VIEW mail_credentials AS
SELECT m.address, p.token_hash,
       ARRAY(SELECT s FROM unnest(m.protocols) AS s
             WHERE s <> 'webmail' AND (p.scope = 'all' OR (p.scope = 'mail' AND s IN ('imap', 'smtp')) OR (p.scope = 'dav' AND s = 'dav'))) AS services
FROM app_passwords p
JOIN mailboxes m ON m.id = p.mailbox_id
JOIN domains d ON d.id = m.domain_id
JOIN users u ON u.id = p.user_id
WHERE p.revoked_at IS NULL AND m.active AND m.kind = 'user' AND d.active AND d.verified_at IS NOT NULL
  AND u.status = 'active'
  AND (m.owner_user_id = u.id OR EXISTS (SELECT 1 FROM mailbox_delegates md WHERE md.mailbox_id = m.id AND md.user_id = u.id AND md.access = 'full' AND md.send_as))
  AND NOT EXISTS (SELECT 1 FROM organization_disabled_features f WHERE f.org_id = m.org_id AND f.feature = 'appPasswords');
