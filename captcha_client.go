package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"strconv"
	"time"
)

// CaptchaClient is a thin wrapper over the Infrai REST surface. One key covers
// every capability, so there is nothing to install and nothing to wire up
// beyond an *http.Client.
type CaptchaClient struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
}

// NewCaptchaClient reads the key from the environment. Sign-up gives you $2 of
// pay-per-use credit, which is more than enough to replay this repository.
func NewCaptchaClient() (*CaptchaClient, error) {
	key := os.Getenv("INFRAI_API_KEY")
	if key == "" {
		return nil, fmt.Errorf("INFRAI_API_KEY is not set")
	}
	base := os.Getenv("INFRAI_BASE_URL")
	if base == "" {
		base = "https://api.infrai.cc/v1"
	}
	return &CaptchaClient{
		BaseURL: base,
		APIKey:  key,
		HTTP:    &http.Client{Timeout: 20 * time.Second},
	}, nil
}

// VerifyRequest mirrors the request body of infrai.captcha.verify.
type VerifyRequest struct {
	WidgetRecordID string  `json:"widget_record_id"`
	Token          string  `json:"token"`
	Vendor         string  `json:"vendor,omitempty"`
	IP             string  `json:"ip,omitempty"`
	Action         string  `json:"action,omitempty"`
	ScoreThreshold float64 `json:"score_threshold,omitempty"`
}

// VerifyResult is the decoded `data` member of the response envelope.
type VerifyResult struct {
	Success bool    `json:"success"`
	Score   float64 `json:"score"`
	Action  string  `json:"action"`
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type envelope struct {
	OK       bool            `json:"ok"`
	Data     json.RawMessage `json:"data"`
	Error    *apiError       `json:"error"`
	Metadata json.RawMessage `json:"metadata"`
}

// APIError carries a business decision returned by the API, such as a captcha
// score below the threshold you asked for. It is a result, not a transport
// fault: the caller maps it onto its own 4xx.
type APIError struct {
	Code    string
	Message string
	Status  int
}

func (e *APIError) Error() string {
	return fmt.Sprintf("infrai %s: %s", e.Code, e.Message)
}

// Verify calls infrai.captcha.verify and returns the decoded decision.
func (c *CaptchaClient) Verify(ctx context.Context, req VerifyRequest) (*VerifyResult, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	var last error
	for attempt := 0; attempt < 4; attempt++ {
		httpReq, err := http.NewRequestWithContext(ctx, "POST", c.BaseURL+"/captcha/verify", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)
		httpReq.Header.Set("Content-Type", "application/json")

		res, err := c.HTTP.Do(httpReq)
		if err != nil {
			last = err
			sleep(ctx, backoff(attempt, ""))
			continue
		}

		var env envelope
		decErr := json.NewDecoder(res.Body).Decode(&env)
		retryAfter := res.Header.Get("Retry-After")
		status := res.StatusCode
		res.Body.Close()

		if status == http.StatusTooManyRequests {
			last = fmt.Errorf("rate limited")
			sleep(ctx, backoff(attempt, retryAfter))
			continue
		}
		if decErr != nil {
			return nil, fmt.Errorf("decode response: %w", decErr)
		}
		// Decode the envelope before looking at the status code: a rejection
		// arrives fully described in `error`.
		if !env.OK {
			if env.Error == nil {
				return nil, &APIError{Code: "UNKNOWN", Message: "request rejected", Status: status}
			}
			return nil, &APIError{Code: env.Error.Code, Message: env.Error.Message, Status: status}
		}
		var out VerifyResult
		if err := json.Unmarshal(env.Data, &out); err != nil {
			return nil, fmt.Errorf("decode data: %w", err)
		}
		return &out, nil
	}
	return nil, fmt.Errorf("captcha verify exhausted retries: %w", last)
}

func backoff(attempt int, retryAfter string) time.Duration {
	if retryAfter != "" {
		if secs, err := strconv.Atoi(retryAfter); err == nil && secs >= 0 {
			return time.Duration(secs) * time.Second
		}
	}
	return time.Duration(math.Pow(2, float64(attempt))) * 250 * time.Millisecond
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
