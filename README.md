# pnotify - process notify

Watches for new processes and sends desktop notifications when they match configurable rules. Useful for alerting on unexpected process launches (e.g. `sudo`, browsers, scripts).

Very tight scoped; but I needed one of these, and vibe coding just made it possible to have it asap.

Alternatives:
- `execsnoop`in bpf tools (bcc tools, depends on your distro).
- proc connector is probably an older way to watch processes; eBPF does everything today. The advantage is that proc connector doesn't require any extra capabilities.

## Requirements

- Go 1.21+
- A running notification daemon (e.g. `dunst`, `mako`)

## Install

```sh
make install
```

Installs the binary to `~/.local/bin/pnotify`, drops a default config at `~/.config/pnotify/config.json` (skipped if one already exists), and enables the systemd user service.

## Config

`~/.config/pnotify/config.json` — array of rules, reloaded automatically on save.

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


## Notes

Refs for proc connector (`NETLINK_CONNECTOR`):
- [kernel/connector.txt](https://www.kernel.org/doc/Documentation/connector/connector.txt), [linux/drivers/connector source](https://github.com/torvalds/linux/blob/master/drivers/connector/)
- [The Proc Connector and Socket Filters - dankwiki](https://nick-black.com/dankwiki/index.php/The_Proc_Connector_and_Socket_Filters)


Note: deduplication isn't necessary; i've not found any duplicates coming into proc connector for one PID. However, I added it while chasing a bug that proved to be a stale instance running. i'm leaving dedup in for now.

