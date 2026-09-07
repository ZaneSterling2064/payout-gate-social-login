package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func at(s string) time.Time { return parseTime(s) }

func TestDecideVerdicts(t *testing.T) {
	signedIn := at("2026-03-02T10:00:00Z")

	cases := []struct {
		name    string
		in      SignIn
		ev      PaymentEvent
		score   float64
		verdict Verdict
	}{
		{
			name:    "small payout on a known device releases",
			in:      SignIn{MemberID: "m_1", Provider: Google, KnownDevice: true, At: signedIn},
			ev:      PaymentEvent{EventID: "e_1", MemberID: "m_1", Kind: "payout", AmountCent: 4_200, At: signedIn.Add(time.Minute)},
			score:   0.62,
			verdict: Release,
		},
		{
			name:    "5k payout needs a higher score than it got",
			in:      SignIn{MemberID: "m_1", Provider: GitHub, KnownDevice: true, At: signedIn},
			ev:      PaymentEvent{EventID: "e_2", MemberID: "m_1", Kind: "payout", AmountCent: 500_000, At: signedIn.Add(time.Minute)},
			score:   0.82,
			verdict: Block,
		},
		{
			name:    "1k payout from an unseen device goes to review",
			in:      SignIn{MemberID: "m_1", Provider: Google, KnownDevice: false, At: signedIn},
			ev:      PaymentEvent{EventID: "e_3", MemberID: "m_1", Kind: "payout", AmountCent: 100_000, At: signedIn.Add(time.Minute)},
			score:   0.95,
			verdict: Review,
		},
		{
			name:    "stale sign-in goes to review",
			in:      SignIn{MemberID: "m_1", Provider: GitHub, KnownDevice: true, At: signedIn},
			ev:      PaymentEvent{EventID: "e_4", MemberID: "m_1", Kind: "payout", AmountCent: 900, At: signedIn.Add(40 * time.Minute)},
			score:   0.9,
			verdict: Review,
		},
		{
			name:    "card_add holds a 0.70 floor regardless of amount",
			in:      SignIn{MemberID: "m_1", Provider: Google, KnownDevice: true, At: signedIn},
			ev:      PaymentEvent{EventID: "e_5", MemberID: "m_1", Kind: "card_add", AmountCent: 0, At: signedIn.Add(time.Minute)},
			score:   0.66,
			verdict: Block,
		},
		{
			name:    "sign-in for another member never authorises the event",
			in:      SignIn{MemberID: "m_2", Provider: Google, KnownDevice: true, At: signedIn},
			ev:      PaymentEvent{EventID: "e_6", MemberID: "m_1", Kind: "payout", AmountCent: 100, At: signedIn.Add(time.Minute)},
			score:   0.99,
			verdict: Block,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := decide(tc.in, tc.ev, tc.score)
			if got.Verdict != tc.verdict {
				t.Fatalf("verdict = %s (%s), want %s", got.Verdict, got.Reason, tc.verdict)
			}
		})
	}
}

func fakeInfrai(t *testing.T, status int, body string) *CaptchaClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v1/captcha/verify" {
			t.Errorf("unexpected call %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q", got)
		}
		var req VerifyRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		} else if req.WidgetRecordID != "widget_1" {
			t.Errorf("widget_record_id = %q, want widget_1", req.WidgetRecordID)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return &CaptchaClient{BaseURL: srv.URL + "/v1", APIKey: "test-key", HTTP: srv.Client()}
}

const sampleBody = `{"sign_in":{"member_id":"m_1","provider":"github","email":"dana@example.com","widget_record_id":"widget_1",
"ip":"203.0.113.9","captcha_token":"tok_live","known_device":true,"at":"2026-03-02T10:00:00Z"},
"event":{"event_id":"evt_9001","member_id":"m_1","kind":"payout","amount_cent":4200,
"currency":"USD","at":"2026-03-02T10:01:00Z"}}`

func post(t *testing.T, g *Gate) (int, gateResponse) {
	t.Helper()
	rec := httptest.NewRecorder()
	g.ServeHTTP(rec, httptest.NewRequest("POST", "/events/decide", strings.NewReader(sampleBody)))
	var out gateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	return rec.Code, out
}

func TestGateReleasesAndReplays(t *testing.T) {
	g := &Gate{
		Captcha: fakeInfrai(t, 200, `{"ok":true,"data":{"success":true,"score":0.81,"action":"payout"},"error":null,"metadata":{}}`),
		Ledger:  NewAuditLedger(),
	}

	code, out := post(t, g)
	if code != 200 || out.Verdict != Release || !out.First {
		t.Fatalf("first call: %d %+v", code, out)
	}

	// The payments stream is at-least-once; the ledger is not.
	code, out = post(t, g)
	if code != 200 || out.Verdict != Release || out.First {
		t.Fatalf("replay: %d %+v", code, out)
	}
	if lines := g.Ledger.Lines(); len(lines) != 1 {
		t.Fatalf("ledger rows = %d, want 1", len(lines))
	}
}

func TestGateAnswers403WhenTheSignInIsRejected(t *testing.T) {
	g := &Gate{
		Captcha: fakeInfrai(t, 422, `{"ok":false,"data":null,"error":{"code":"VERIFICATION_REJECTED","message":"sign-in was not verified"},"metadata":{}}`),
		Ledger:  NewAuditLedger(),
	}
	code, out := post(t, g)
	if code != http.StatusForbidden || out.Verdict != Block {
		t.Fatalf("got %d %+v, want 403 block", code, out)
	}
}
