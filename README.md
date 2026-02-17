# pnotifier

Watches for new processes and sends desktop notifications when they match configurable rules. Useful for alerting on unexpected process launches (e.g. `sudo`, browsers, scripts).

## Requirements

- Go 1.21+
- A running notification daemon (e.g. `dunst`, `mako`)

## Install

```sh
make install
```

Installs the binary to `~/.local/bin/notifier`, drops a default config at `~/.config/pnotifier/config.json` (skipped if one already exists), and enables the systemd user service.

## Config

`~/.config/pnotifier/config.json` — array of rules, reloaded automatically on save.

```json
[
  {
    "name": "sudo running",
    "match": {
      "name_regex": "sudo",
      "cmdline_contains": [],
      "username": ""
    },
    "notify_title": "sudo requested",
    "notify_body": "PID {pid}: {cmdline}",
    "urgency": "critical"
  }
]
```

| Field | Description |
|---|---|
| `match.name_regex` | Case-insensitive regex matched against the process name |
| `match.cmdline_contains` | All strings must appear in the command line |
| `match.username` | Restrict to a specific user (omit to match any) |
| `notify_body` | Supports `{name}`, `{pid}`, `{cmdline}`, `{username}` |
| `urgency` | `low`, `normal`, or `critical` |

## Uninstall

```sh
make uninstall
```

Stops and removes the service and binary. Config is left untouched.
