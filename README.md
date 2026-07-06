# agy-acp

ACP stdio adapter for Google Antigravity.

## Usage

```sh
agy-acp --auth=auto
agy-acp --auth=oauth
GEMINI_API_KEY=... agy-acp --auth=api-key
```

## Auth

- `oauth`: uses `agy` through `agy-go`, so it reuses Antigravity CLI OAuth.
- `api-key`: uses the official Python SDK through `agy-go`, so it needs `google-antigravity` installed and `GEMINI_API_KEY` set.
- `auto`: prefers working CLI OAuth, then falls back to `GEMINI_API_KEY`.

## ACP Surface

- `initialize`
- `session/new`
- `session/load` / `session/resume`
- `session/prompt`
- `session/cancel`
- `session/close`
- `session/set_mode` with `full-access` and `plan`
- `session/set_config_option` for `model` and `reasoning_effort`
