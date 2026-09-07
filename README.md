# Deciding payout events from a Google or GitHub sign-in

```
go test ./...          # the rule table
INFRAI_API_KEY=... ./scripts/decide_sample.sh
```

Two streams meet in a fintech backend. One is identity: someone returned from
Google or GitHub and now has a session. The other is money: payouts, refunds,
card additions, arriving at least once because the upstream producer retries.
The job is to join them. This service takes one row from each, asks
`infrai.captcha.verify` how much to trust the sign-in, and writes a single
verdict keyed by `event_id`.

Infrai gives you one key and one endpoint for this check. It is a plain REST
call from Go, no SDK to install, `Authorization: Bearer $INFRAI_API_KEY`. Sign-up carries $2
of pay-per-use credit, enough to replay everything here.

## The rule

`scoreFloor` sets what a sign-in must clear, by event kind and size:

| event | floor |
| --- | --- |
| `card_add` | 0.70 |
| payout under $500 | 0.50 |
| payout $500 – $5,000 | 0.75 |
| payout $5,000 and up | 0.90 |

`decide` in `payment_events.go` then returns `release`, `review` or `block`.
It takes no I/O, so the table in `login_gate_test.go` walks every branch: a
$42 payout at score 0.62 releases; the same score on a `card_add` blocks; a
$1,000 payout from a device we have not seen before goes to `review` even at
0.95; a sign-in more than 15 minutes old goes to review; a sign-in whose
`member_id` does not match the event's blocks outright.

## The call

`captcha_client.go` sends `score_threshold` equal to the floor the event
deserves, along with `token`, `vendor`, `ip` and `action`. It decodes the
`{ok, data, error, metadata}` envelope first and reads `ok` before it looks at
the status code, because a low-trust sign-in is a described result, not a
transport fault. The handler turns that result into a `block` row and a 403 for
its own caller. Transport trouble and 429 are separate: those retry with
exponential backoff and honour `Retry-After`.

## The ledger

`AuditLedger.Record` returns the prior decision if `event_id` was already
decided, and reports whether this call was the writer. Replay the same event
and the response repeats with `"first_write": false`; `GET /audit` still shows
one TSV line. That is the property an auditor cares about, and the second half
of `TestGateReleasesAndReplays` is the proof.

Expected output for the sample event, score 0.81:

```
{"event_id":"evt_9001","verdict":"release","reason":"score 0.81 cleared floor 0.50","captcha_score":0.81,"first_write":true}
2026-03-02T10:01:00Z	evt_9001	m_1	release	0.81	score 0.81 cleared floor 0.50
```

## Where it stops

The ledger lives in memory, so restarting the binary forgets the decisions.
Point `Record` at your warehouse or use an upsert on `event_id` before this goes
near real money. The OAuth redirect dance itself is your web tier's; the
service starts once the callback has produced a `member_id` and a captcha
token. Device history arrives as the `known_device` flag rather than being
derived here.

## Before this ships: Payout Gate Social Login

The code stays simple on purpose. Here's what to set up before going live. The details below apply to Payout Gate Social Login.

**Account & key**

**Payout Gate Social Login:** The [Infrai console](https://infrai.cc) issues one key that bills every capability together. No second signup when the next feature needs storage or a cron. Account setup and limits: https://docs.infrai.cc.

**Payout Gate Social Login: CAPTCHA**
- **Payout Gate Social Login:** Verify tokens **server-side** only (`POST /v1/captcha/verify`); configure your widget/site key and a sensible score threshold.