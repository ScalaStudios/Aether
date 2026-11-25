ALTER TABLE domains ADD COLUMN coexistence_host text NOT NULL DEFAULT '';
ALTER TABLE mailboxes ADD COLUMN coexistence_dual boolean NOT NULL DEFAULT false;

CREATE VIEW mail_coexistence_domains AS
SELECT d.name, d.coexistence_host AS host
FROM domains d
JOIN mail_domains md ON md.name = d.name
WHERE d.coexistence_host <> '';

CREATE VIEW mail_coexistence_reserved AS
SELECT m.address
FROM mailboxes m
JOIN domains d ON d.id = m.domain_id
JOIN mail_coexistence_domains c ON c.name = d.name
WHERE NOT m.active
UNION ALL
SELECT a.address
FROM aliases a
JOIN domains d ON d.id = a.domain_id
JOIN mail_coexistence_domains c ON c.name = d.name
WHERE NOT a.active;

CREATE VIEW mail_coexistence_dual AS
SELECT mu.address
FROM mail_users mu
JOIN mailboxes m ON m.id = mu.id
JOIN domains d ON d.id = m.domain_id
JOIN mail_coexistence_domains c ON c.name = d.name
WHERE m.coexistence_dual;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'aether_mail') THEN
        GRANT SELECT ON mail_coexistence_domains, mail_coexistence_reserved, mail_coexistence_dual TO aether_mail;
    END IF;
END $$;
