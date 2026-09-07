package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

// gateRequest is the joined row the service accepts: the social sign-in that
// just completed, and the payment event it is meant to authorise.
type gateRequest struct {
	SignIn struct {
		MemberID       string `json:"member_id"`
		Provider       string `json:"provider"`
		Email          string `json:"email"`
		IP             string `json:"ip"`
		WidgetRecordID string `json:"widget_record_id"`
		CaptchaToken   string `json:"captcha_token"`
		KnownDevice    bool   `json:"known_device"`
		At             string `json:"at"`
	} `json:"sign_in"`
	Event struct {
		EventID    string `json:"event_id"`
		MemberID   string `json:"member_id"`
		Kind       string `json:"kind"`
		AmountCent int64  `json:"amount_cent"`
		Currency   string `json:"currency"`
		At         string `json:"at"`
	} `json:"event"`
}

type gateResponse struct {
	EventID string  `json:"event_id"`
	Verdict Verdict `json:"verdict"`
	Reason  string  `json:"reason"`
	Score   float64 `json:"captcha_score"`
	First   bool    `json:"first_write"`
}

// Gate holds the client and the ledger for the running service.
type Gate struct {
	Captcha *CaptchaClient
	Ledger  *AuditLedger
}

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// ServeHTTP decides one payment event and appends the verdict to the ledger.
func (g *Gate) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "use POST", http.StatusMethodNotAllowed)
		return
	}
	var req gateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "malformed body"})
		return
	}

	in := SignIn{
		MemberID:     req.SignIn.MemberID,
		Provider:     Provider(req.SignIn.Provider),
		Email:        req.SignIn.Email,
		IP:           req.SignIn.IP,
		CaptchaToken: req.SignIn.CaptchaToken,
		KnownDevice:  req.SignIn.KnownDevice,
		At:           parseTime(req.SignIn.At),
	}
	ev := PaymentEvent{
		EventID:    req.Event.EventID,
		MemberID:   req.Event.MemberID,
		Kind:       req.Event.Kind,
		AmountCent: req.Event.AmountCent,
		Currency:   req.Event.Currency,
		At:         parseTime(req.Event.At),
	}
	if in.Provider != Google && in.Provider != GitHub {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "provider must be google or github"})
		return
	}
	if ev.EventID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "event_id is required"})
		return
	}

	// Already decided? Replay the stored verdict instead of re-charging the
	// event through the gate.
	if prior, ok := g.Ledger.rows[ev.EventID]; ok {
		writeJSON(w, http.StatusOK, gateResponse{prior.EventID, prior.Verdict, prior.Reason, prior.CaptchaScore, false})
		return
	}

	floor := scoreFloor(ev.Kind, ev.AmountCent)
	res, err := g.Captcha.Verify(r.Context(), VerifyRequest{
		WidgetRecordID: req.SignIn.WidgetRecordID,
		Token:          in.CaptchaToken,
		Vendor:         "recaptcha",
		IP:             in.IP,
		Action:         ev.Kind,
		ScoreThreshold: floor,
	})
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) {
			// A decision the API made for us: record it and answer the caller
			// with a 4xx of our own.
			d, first := g.Ledger.Record(Decision{
				EventID: ev.EventID, MemberID: ev.MemberID, Provider: in.Provider,
				Verdict: Block, Reason: apiErr.Code, At: ev.At,
			})
			writeJSON(w, http.StatusForbidden, gateResponse{d.EventID, d.Verdict, d.Reason, d.CaptchaScore, first})
			return
		}
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "identity check unavailable"})
		return
	}

	d, first := g.Ledger.Record(decide(in, ev, res.Score))
	status := http.StatusOK
	if d.Verdict == Block {
		status = http.StatusForbidden
	}
	writeJSON(w, status, gateResponse{d.EventID, d.Verdict, d.Reason, d.CaptchaScore, first})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
