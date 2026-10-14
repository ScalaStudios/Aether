# Third-party components

Aether's own code is AGPL-3.0-or-later. The components below are either Go or npm dependencies that are compiled into Aether's binaries, or separate services that run in their own containers.

## Services

| Component | License | How Aether uses it |
| --- | --- | --- |
| Postfix 3.7 (Debian 12) | IPL-1.0 or EPL-2.0 | Separate process, configured by files |
| Dovecot 2.3 + Pigeonhole (Debian 12) | MIT / LGPL-2.1 | Separate process; Aether talks to it over IMAP |
| Rspamd | Apache-2.0 | Separate process, connected to Postfix as a milter |
| PostgreSQL 17 | PostgreSQL License | Separate process; Aether talks to it over the database protocol |
| Valkey 8 | BSD-3-Clause | Separate process; Aether talks to it over the Redis protocol |
| Caddy 2 | Apache-2.0 | Reverse proxy |

## Go modules

| Module | License |
| --- | --- |
| github.com/jackc/pgx/v5 | MIT |
| github.com/coreos/go-oidc/v3 | Apache-2.0 |
| github.com/go-jose/go-jose/v4 | Apache-2.0 |
| golang.org/x/oauth2, golang.org/x/crypto | BSD-3-Clause |
| github.com/emersion/go-imap/v2, go-message, go-smtp, go-sasl | MIT |
| github.com/microcosm-cc/bluemonday | BSD-3-Clause |
| github.com/redis/go-redis/v9 | BSD-2-Clause |

## Web

Next.js, React, TanStack Query, React Hook Form, Zod, Radix UI, shadcn/ui, lucide-react, sonner, and next-themes are MIT licensed. Tiptap's core packages are also MIT.

The fonts Archivo, Inter, and JetBrains Mono are licensed under the SIL Open Font License 1.1. Their licence files are shipped in `brand/fonts/`.

The Scala Studios logo and brand assets in `brand/` are trademarks of Scala Studios. They are not covered by the AGPL, and forks must not use them to imply endorsement.

## Before a release

Before each release, generate an up-to-date report:

```sh
go run github.com/google/go-licenses@latest report ./cmd/aether
cd apps/web && npx license-checker --production --summary
```
