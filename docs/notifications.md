# Notifications

Tracer turns collector events into ntfy, Telegram, or Forgejo messages. A
notifier is one destination; subscriptions decide which collector/app events
reach it.

## Pipeline

1. The collector sends error logs, uptime transitions, lifecycle events, and
   resource stats to the panel over its WebSocket.
2. Panel ingest creates a notification for each event:
   - `alert` — an error log entry
   - `uptime` — an app health status change
   - `lifecycle` — an app start, restart, or exit
   - `resource` — CPU, memory, or disk usage above its threshold
3. The notification service finds every enabled notifier with a matching
   subscription, filters them by the owner's app access policy, and renders the
   message.
4. The dispatcher sends matching notifications asynchronously, up to five at a
   time. A failing destination is logged and does not block the others.

## Subscriptions and wildcards

A subscription is a collector/app pair. Either part may be `*`:

| Subscription | Matches |
|---|---|
| `collector/app` | one app on one collector |
| `*/app` | that app on every collector |
| `collector/*` | every app on one collector |
| `*/*` | every notification from every collector |

A `*/*` subscription carries error and lifecycle alerts as well as uptime and
resource alerts. In particular it matches resource and uptime notifications,
whose app name is empty. Such a notification matches `*/*` and `collector/*`,
but not `*/app` or `collector/app`; an agent subscribed only to a concrete app
misses collector-wide alerts. A global agent needs `*/*` to see them all. User
access still applies after matching: an admin receives everything, while a
regular user only receives collectors and apps their access policy allows.

The notifier form offers `*:*` as **all collectors, all apps** and a
`collector:*` row in every accessible collector group. The form shows stored
wildcard rows whose collector is no longer listed, so an unchanged save keeps
them. A `*` collector with a concrete app (`*/app`) is still available through
MCP `add_notifier_subscription`, but the panel form does not render that row.

## Dropped log lines

The four matches above are the whole routing model. Because `*/*` carries every
log alert from every app, it treats the log stream itself as an alert channel.
An app whose output cannot be parsed is therefore a denial of that channel, not
a source of extra alerts: the collector counts the unusable lines and never
reports each one at error level. A broken parser or a mixed log format must not
flood the topic that is supposed to carry real incidents.

The collector keeps a per-app count for its current stats interval. It emits at
most one warning per app for the interval, carrying the interval count, the
lifetime total, and a 200-byte sample of the last dropped line. For its own
journal unit (`tracer-collector`) it suppresses that warning entirely, so
collecting its own stream cannot generate alert traffic; the counts still flow
to the panel.

The panel accumulates the interval counts it receives on the existing stats
event and exposes them as the app's **Dropped** card. A nonzero value is a
signal to inspect the app's log format; it is not a notification and does not
create a panel log entry.

## Resource thresholds

A fresh panel config defaults CPU, memory, and disk thresholds to **95 %** and
the notification cooldown to **10 minutes**. A threshold of `0` disables that
resource check. Per-collector overrides take precedence over the panel-wide
values.

Existing panel configs store their threshold values explicitly, so changing the
defaults does not update a running panel. Set the live values explicitly in
Settings or with a form POST to `/settings/config`:

```text
cpu_threshold_percent=95
mem_threshold_percent=95
disk_threshold_percent=95
resource_notify_minutes=10
```

`alert_delay` stays `0s`; no restart is needed or used to force an alert.

## Message headers

- An app notification starts with `app@collector`, for example
  `backend-production@cyberpass`. An uptime transition reads
  `status changed: down -> up`.
- A notification without an app, such as a resource alert, starts with the
  collector alone, for example `cyberpass` rather than `@cyberpass`.

## Example: an ntfy topic for an agent

Create the notifier over MCP; the welcome message creates the ntfy topic:

```json
{
  "type": "ntfy",
  "name": "tracer-pi-ui",
  "url": "https://ntfy.naqerl.com",
  "topic": "pi-ui"
}
```

Subscribe it to everything with one MCP `add_notifier_subscription` call
(replace `6` with the id returned by `create_notifier`):

```json
{
  "notifier_id": 6,
  "collector": "*",
  "app": "*"
}
```

The Pi agent receives the topic's messages through its `ntfy-mcp` server. Add
this entry to the agent's subscription file (`/home/pi/.pi/agent/subscriptions.json`
for the Pi setup):

```json
{
  "server": "ntfy-mcp",
  "name": "ntfy.message",
  "arguments": {
    "topic": "pi-ui"
  },
  "title": "Tracer alerts"
}
```

Then reload subscriptions with `reload target=subscriptions`. `GET /api/state`
shows the stable id of the loaded subscription. Because the notifier uses
`*/*`, the agent receives error and lifecycle alerts in addition to uptime and
resource messages.

## Verification

Run the focused unit tests:

```bash
go test ./apps/panel/internal/services/notify/ ./apps/panel/internal/config/
```

They cover the `*/*` match for a resource notification with an empty app name,
the collector-only header, and the 95 %/10-minute defaults.

For an end-to-end check, bring an app down and back up: the topic receives
`backend-production@cyberpass` with body `status changed: down -> up`. To check
a resource alert without waiting for a real 95 % breach, temporarily set
`disk_threshold_percent=85`, wait for one `resource` message whose first line is
`cyberpass`, then restore `disk_threshold_percent=95`. The notification carries
the breached resource and its usage; no restart is required.
