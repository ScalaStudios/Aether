# Translations

Aether Mail, the admin console and server-generated emails support multiple languages, with English (`en`), Portuguese (`pt`) and Spanish (`es`) built in.

## How a language is chosen

Aether picks the active language from the first setting that matches a supported locale:

1. **Personal language** set under **Settings → Profile** (`PATCH /api/v1/auth/me` with `locale`).
2. **Organization default language** set under **Administration → General** (`PATCH /api/v1/organizations/{orgId}` with `defaultLocale`, or on creation with `POST /api/v1/organizations`).
3. **Browser language** (`navigator.languages` in the web app, `Accept-Language` on the API).
4. **English (`en`)** as the final fallback.

When a person leaves their personal language on **Default**, Aether uses their organization's default language when set, or falls back to the browser language and then English. On the sign-in page, Aether uses the domain's organization default language when branding resolves an organization for the host, and otherwise uses the browser language.

Missing keys in any translation catalog fall back cleanly to the English string for that key, so a partial translation never renders raw keys or blanks.

## Server-generated emails and profiles

The Go API translates outgoing system messages using the embedded catalogs in `internal/i18n/locales/`:

| Message | Locale used |
| --- | --- |
| Calendar invitations, updates, replies and cancellations | Each recipient's personal language when they have an account in Aether, then their organization's default, then the sender's organization default, then English. Recipients with different languages receive separate localized copies. |
| Automatic reply default subject | The mailbox owner's language, then the mailbox organization's default, then English, when no custom subject is set. |
| Apple configuration profile (`.mobileconfig`) | The signed-in user's language, then their organization's default, then `Accept-Language`, then English. |
| Notification, quota warning and digest templates | Resolved per recipient through `internal/i18n.T`. |

## Translation file layout

All translatable strings live in standard nested UTF-8 JSON files keyed by BCP 47 language code:

- **Web app (Aether Mail and admin console):** `apps/web/src/messages/<locale>.json`
- **API (server-generated emails, dates and Apple profiles):** `internal/i18n/locales/<locale>.json`

English (`en.json`) is the source catalog in both locations.

### Placeholders and plurals

- Named placeholders use single curly braces, such as `{name}`, `{count}`, `{address}` or `{title}`. Keep placeholder names identical to `en.json`; you may move them anywhere in the sentence to fit the target language's grammar.
- Counted phrases use sibling keys suffixed with `_one` and `_other` (for example `messageCount_one` and `messageCount_other`), selected with `Intl.PluralRules` in the web app.

## Translating with Weblate or Crowdin

Both JSON directories use the standard nested key-value JSON format supported natively by Weblate (Nested JSON) and Crowdin (JSON).

When connecting a translation platform to the repository, configure two components with English as the base file:

| Component | Base file | Translation file mask | Format |
| --- | --- | --- | --- |
| Web app | `apps/web/src/messages/en.json` | `apps/web/src/messages/%language%.json` | Nested JSON |
| Server emails | `internal/i18n/locales/en.json` | `internal/i18n/locales/%language%.json` | Nested JSON |

Configure the platform to treat `{variable}` tokens as placeholders so translators receive warnings if a placeholder is renamed or omitted.

## Adding a new language

1. Copy `apps/web/src/messages/en.json` to `apps/web/src/messages/<code>.json` and translate the values.
2. Copy `internal/i18n/locales/en.json` to `internal/i18n/locales/<code>.json` and translate the values.
3. Register the new locale code and its native label in `apps/web/src/lib/i18n.tsx` (`SUPPORTED_LOCALES`, `LOCALE_LABELS` and `catalogs`) and in `internal/i18n/i18n.go` (`supported` and `SupportedLocales`).
4. Add the new code to the `locale` and `defaultLocale` enum lists in `api/openapi.yaml`.
5. Run the test suites to verify catalog loading, fallback behavior and builds:

```bash
go test ./internal/i18n/... ./internal/api/...
cd apps/web && npx tsc --noEmit && npm run lint && npm run build
```
