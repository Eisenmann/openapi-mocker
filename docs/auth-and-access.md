# Auth simulation and access control

## Auth simulation

The `Auth simulation` panel of a project (or
`GET/PUT/DELETE /api/projects/{id}/auth`) lets clients test their 401 and
403 paths without an identity provider. Modes: `off` (default),
`contract` (enforce the security requirements the contract declares per
operation or globally: API key in header / query / cookie, HTTP bearer
and basic, OAuth2 and OpenID Connect with required scopes; operations
with `security: []` stay public) and `all` (every request needs a bearer
token, basic credentials or an API key in `apiKeyHeader` /
`?api_key=`). Missing credentials give `401` with a `WWW-Authenticate`
challenge, unknown ones `401 invalid_token`, missing scopes
`403 insufficient_scope`. Configure fixed `credentials` (tokens and API
keys), `users` (basic auth and the password grant) and OAuth2 `clients`.
Each can carry a **scenario** that decides what its holder gets: `expired`
and `revoked` answer 401, `forbidden` and `insufficient_scope` answer 403.
A client's scenario applies to the tokens it is issued. An empty `scopes`
list means every scope; with no users or clients configured, any
credentials are accepted. The **token endpoint** is
`POST /mock/{projectId}<tokenPath>` (default `/oauth/token`) with the
`client_credentials`, `password`, `authorization_code` (any code works,
except `invalid`, `expired` and `used`) and `refresh_token` grants, form
or JSON body, client credentials in the body or a Basic header. Issued
tokens last `tokenTtlSeconds` (default 3600), then answer 401 as expired.
Refresh tokens work once. Issued tokens live in memory and are lost on
restart; the configured credentials are stored with the project and
travel in bundles. Rejections show in the request log with source `auth`.
GraphQL (`POST /mock/{projectId}/graphql`) and MCP
(`POST /mock/{projectId}/mcp`) endpoints are guarded too. Their contracts
carry no security requirements, so in either enabled mode the whole
endpoint needs a bearer token, basic credentials or an API key, and the
same scenarios apply (401 with `WWW-Authenticate`, 403). Per-operation
scopes are not checked there. The token endpoint is served for these
projects as well.

## Access control and audit

Every project can have API tokens and
read-only share links. A project is **open until its first token is
created**; from then on every `/api/projects/{id}/...` and
`/api/mocks/{id}` call needs `Authorization: Bearer omk_...`.
Roles: `viewer` (read) < `load-runner` (run load tests) < `editor` (change contracts, mocks, auth, export)
< `admin` (manage tokens and share links). Secrets are shown once and only
their SHA-256 hash is stored; tokens can expire (`ttlDays`) and be revoked.
**Share links** (`oms_...`) are read-only: send them as `X-Share-Token`
or `?share=` on GET requests; `GET /api/share/{token}` returns a JSON
snapshot (project, contract, mocks). Share links cannot read the audit
log or auth settings. The optional `ADMIN_TOKEN` environment variable is a
superuser/recovery token that works for every project. The **audit log**
(`GET /api/projects/{id}/audit?limit&action&actor&since`) records who
changed contracts, mocks, settings, tokens and links, plus denied
changes (`access.denied`); it keeps the latest 2000 entries per project
and survives project deletion. The UI has an "Access and audit" panel and
asks for a token once per project when the API answers 401.
