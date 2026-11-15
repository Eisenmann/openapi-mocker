# Import and export

## Import and export

`Import…` in the sidebar (or `POST /api/import`,
the file as the request body or a multipart `file` field, optional
`?name=`, up to 32 MB) creates a new project from:
a **Postman** collection (v2.x), an **Insomnia** export (v4 JSON or v5
YAML), a **HAR** file, an **OpenAPI 3.0/3.1 or Swagger 2** contract
(JSON or YAML) or an **openapi-mocker bundle**. For collections and HAR
files the contract is inferred (templated paths such as `/users/{userId}`,
path and query parameters, request bodies and response schemas merged from
the examples) and every saved response becomes a mock rule: the lowest 2xx
is the default scenario, the other status codes are scenarios named
`status-<code>`. HAR entries that are not API calls (assets, preflights,
`304`) are skipped; `Set-Cookie`, `Authorization` and similar headers are
never imported. Swagger 2.0 and OpenAPI 3.1 contracts are converted to
OpenAPI 3.0.3 when published or validated (also in the Contract tab); the
conversion note is shown, and 3.1-only keywords with no 3.0 equivalent
(`prefixItems`, `if/then/else`, `webhooks`, ...) are dropped and listed.
Export: `GET /api/projects/{id}/export?format=bundle|postman|insomnia`
(`&history=1` puts every contract version into the bundle) and
`GET /api/projects/{id}/contract/export?format=openapi3|openapi31|swagger2&as=yaml|json`.
A **bundle** is a single JSON file with the project settings, the
contract(s) and the mock rules, for sharing; import it to get a copy.
Mock state, logs, drift reports, LLM providers and the upstream URL are not
included. Recorded bodies in HAR files and collections can contain secrets:
review them before sharing a bundle.
