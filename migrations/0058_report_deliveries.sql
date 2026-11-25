CREATE TABLE report_deliveries (
    kind text NOT NULL,
    day date NOT NULL,
    domain text NOT NULL,
    destination text NOT NULL,
    PRIMARY KEY (kind, day, domain, destination)
);
