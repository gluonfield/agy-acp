# agy-acp

ACP stdio adapter for Google Antigravity.

## Usage

```sh
agy-acp --auth=auto
agy-acp --auth=oauth
agy-acp --agy=/path/to/agy
```

## Auth

- `oauth`: uses `agy` through `agy-go`, so it reuses Antigravity CLI OAuth.
- `auto`: uses the same CLI backend, checking OAuth status when the adapter starts.

## ACP Surface

- `initialize`
- `session/new`
- `session/load` / `session/resume`
- `session/prompt`
- `session/cancel`
- `session/close`
- `session/set_mode` with `full-access` and `plan`
- `session/set_config_option` for `model` and `reasoning_effort`
