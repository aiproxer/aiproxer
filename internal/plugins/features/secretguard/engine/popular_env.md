# Popular secret environment names

Loaded when `single_user.include_popular_env` is true.

## Matching

1. **Exact fallback** — names in `PopularSecretEnvNames` (e.g. `AWS_SECRET_ACCESS_KEY`, `STRIPE_SECRET_KEY`, `AZURE_CLIENT_SECRET`).
2. **Inferred** — case-sensitive uppercase env names ending in `_API_KEY` or `_TOKEN` (covers Context7, Apify, Tavily, and similar without enumerating each service).

Proxy credential env names keep the `proxy_env` category even when they also match inference.

## Frontend public prefixes (not inferred)

These prefixes are excluded from generic inference (still loadable via `include_env`):

`NEXT_PUBLIC_`, `VITE_`, `PUBLIC_`, `EXPO_PUBLIC_`, `REACT_APP_`, `GATSBY_`, `NUXT_PUBLIC_`, `VUE_APP_`

## Anti-CSRF segments (not inferred)

Underscore-delimited segments `CSRF`, `XSRF`, and `CRSF` are excluded from generic inference (e.g. `CSRF_TOKEN`, `ANTI_CSRF_TOKEN`). Substring-only names such as `MYCSRF_TOKEN` still infer. Exact `PopularSecretEnvNames` and `include_env` still win; `exclude_env` wins over every source.

## Explicitly excluded (unless `include_env`)

IDs, profiles, or filesystem paths — not secret values suitable for exact-match scanning:

- `AWS_ACCESS_KEY_ID`
- `AWS_PROFILE`
- `GOOGLE_APPLICATION_CREDENTIALS`
- `CURL_CA_BUNDLE`
