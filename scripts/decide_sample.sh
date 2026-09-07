#!/usr/bin/env bash
# Start the gate, push one payout event through it, print the audit ledger.
set -euo pipefail

: "${INFRAI_API_KEY:?set INFRAI_API_KEY first}"
: "${INFRAI_WIDGET_RECORD_ID:?set INFRAI_WIDGET_RECORD_ID first}"
ADDR="${ADDR:-:8080}"
HOST="http://localhost${ADDR}"

go build -o /tmp/login-gate . 
ADDR="$ADDR" /tmp/login-gate &
PID=$!
trap 'kill "$PID"' EXIT
sleep 1

curl -s -X POST "$HOST/events/decide" \
  -H 'Content-Type: application/json' \
  -d '{
    "sign_in": {"member_id":"m_1","provider":"github","email":"dana@example.com",
      "widget_record_id":"'"${INFRAI_WIDGET_RECORD_ID}"'",
      "ip":"203.0.113.9","captcha_token":"'"${CAPTCHA_TOKEN:-tok_from_your_widget}"'",
      "known_device":true,"at":"2026-03-02T10:00:00Z"},
    "event": {"event_id":"evt_9001","member_id":"m_1","kind":"payout",
      "amount_cent":4200,"currency":"USD","at":"2026-03-02T10:01:00Z"}
  }'
echo
curl -s "$HOST/audit"
