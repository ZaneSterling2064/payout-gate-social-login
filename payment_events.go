package main

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Provider is the social identity provider that signed the member in.
type Provider string

const (
	Google Provider = "google"
	GitHub Provider = "github"
)

// SignIn is one row off the identity stream: whoever just came back from the
// provider, plus the captcha token their browser carried.
type SignIn struct {
	MemberID     string
	Provider     Provider
	Email        string
	IP           string
	CaptchaToken string
	KnownDevice  bool
	At           time.Time
}

// PaymentEvent is one row off the payments stream, keyed by EventID so a
// replayed batch folds onto the same ledger entry.
type PaymentEvent struct {
	EventID    string
	MemberID   string
	Kind       string // payout, refund, card_add
	AmountCent int64
	Currency   string
	At         time.Time
}

// Verdict is what the gate decided, and it is what gets written down.
type Verdict string

const (
	Release Verdict = "release"
	Review  Verdict = "review"
	Block   Verdict = "block"
)

// Decision joins the two streams for one payment event.
type Decision struct {
	EventID      string
	MemberID     string
	Provider     Provider
	Verdict      Verdict
	CaptchaScore float64
	Reason       string
	At           time.Time
}

// scoreFloor is the score a sign-in must clear before a payout of this size
// moves. Larger money, stricter floor.
func scoreFloor(kind string, amountCent int64) float64 {
	if kind == "card_add" {
		return 0.7
	}
	switch {
	case amountCent >= 500_000:
		return 0.9
	case amountCent >= 50_000:
		return 0.75
	default:
		return 0.5
	}
}

// decide is the whole business rule, kept free of I/O so the table test can
// hit every branch. score is the value captcha.verify reported for the
// sign-in that authorised this event.
func decide(in SignIn, ev PaymentEvent, score float64) Decision {
	d := Decision{
		EventID:      ev.EventID,
		MemberID:     ev.MemberID,
		Provider:     in.Provider,
		CaptchaScore: score,
		At:           ev.At,
	}
	floor := scoreFloor(ev.Kind, ev.AmountCent)
	switch {
	case in.MemberID != ev.MemberID:
		d.Verdict, d.Reason = Block, "sign-in belongs to a different member"
	case score < floor:
		d.Verdict, d.Reason = Block, fmt.Sprintf("score %.2f under floor %.2f", score, floor)
	case !in.KnownDevice && ev.AmountCent >= 50_000:
		d.Verdict, d.Reason = Review, "large amount from an unseen device"
	case ev.At.Sub(in.At) > 15*time.Minute:
		d.Verdict, d.Reason = Review, "sign-in older than 15m"
	default:
		d.Verdict, d.Reason = Release, fmt.Sprintf("score %.2f cleared floor %.2f", score, floor)
	}
	return d
}

// AuditLedger keeps one decision per EventID, so an at-least-once payment
// stream produces an at-most-once audit trail.
type AuditLedger struct {
	rows map[string]Decision
}

func NewAuditLedger() *AuditLedger { return &AuditLedger{rows: map[string]Decision{}} }

// Record writes the decision unless the event was already decided, and reports
// whether this call was the one that wrote it.
func (l *AuditLedger) Record(d Decision) (Decision, bool) {
	if prior, ok := l.rows[d.EventID]; ok {
		return prior, false
	}
	l.rows[d.EventID] = d
	return d, true
}

// Lines renders the ledger for an auditor: stable order, one event per line.
func (l *AuditLedger) Lines() []string {
	out := make([]string, 0, len(l.rows))
	for _, d := range l.rows {
		out = append(out, fmt.Sprintf("%s\t%s\t%s\t%s\t%.2f\t%s",
			d.At.UTC().Format(time.RFC3339), d.EventID, d.MemberID, d.Verdict, d.CaptchaScore, d.Reason))
	}
	sort.Strings(out)
	return out
}

func (l *AuditLedger) String() string { return strings.Join(l.Lines(), "\n") }
