# report-service

Turns the event stream into adherence reports and delivers them on a schedule.

Four moving parts, each with a distinct failure mode:

**Projector** — consumes medication events from NATS and increments the
`daily_adherence` rollup as they arrive. Near-real-time, and lossy by design if
an event arrives for a device that was never registered: such events cannot be
attributed to anyone, so they are dropped with a warning rather than retried
forever.

**Reconciler** — nightly, recomputes whole days of `daily_adherence` from the
`events` table, healing any drift the projector left behind. It is the reason
the projector is allowed to be approximate.

**Scheduler** — polls `report_subscriptions` for rows that are due, renders the
report as HTML and CSV, publishes a `medsage.commands.email.send` command on
NATS, and advances `next_run_at`. It does not send email itself;
`notifications-service` does.

**HTTP API** — adherence, activity, event-summary and audit reports, each in
JSON and CSV, behind `authkit`.

## Seeing a report without hardware

The rollup is normally filled by live device events, so a dev box with no
dispenser has nothing to report on. From the repo root:

```sh
make devices-list
make report-demo DEVICE_ID=<uuid>            # 14 days at ~85% adherence
make report-demo DEVICE_ID=<uuid> REPORT_ARGS="-days 30 -adherence 0.6"
```

That seeds plausible events, drives the **real** reconciler, and writes the
report three ways — the JSON the API serves, the CSV that gets attached, and
the HTML body a subscriber receives. It writes events rather than rollup rows
on purpose: it exercises the code that heals production data instead of a
shortcut that would hide bugs in it.

## Subscriptions

Created through the GraphQL API (`createReportSubscription`), not by hand. The
first report goes out on the next scheduler tick rather than a cadence later.

## Tests

```sh
go test ./...
```

Cannot be built outside the superproject: `go.mod` replaces `medsage/authkit`
with `../auth` and `medsage/proto` with `../proto/gen/go`.
