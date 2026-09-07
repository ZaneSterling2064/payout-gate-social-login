package main

import (
	"log"
	"net/http"
	"os"
)

func main() {
	client, err := NewCaptchaClient()
	if err != nil {
		log.Fatal(err)
	}
	gate := &Gate{Captcha: client, Ledger: NewAuditLedger()}

	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":8080"
	}
	mux := http.NewServeMux()
	mux.Handle("/events/decide", gate)
	mux.HandleFunc("/audit", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/tab-separated-values")
		for _, line := range gate.Ledger.Lines() {
			w.Write([]byte(line + "\n"))
		}
	})

	log.Printf("gate listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}
