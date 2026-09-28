package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDoResendsBodyOnRetry(t *testing.T) {
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		if len(bodies) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewGitHubClient("tok")
	resp, err := c.do("PUT", srv.URL, []byte(`{"ignored":true}`))
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	resp.Body.Close()

	if len(bodies) != 2 {
		t.Fatalf("got %d requests, want 2 (rate-limit retry)", len(bodies))
	}
	for i, b := range bodies {
		if b != `{"ignored":true}` {
			t.Errorf("attempt %d body = %q, want %q", i+1, b, `{"ignored":true}`)
		}
	}
}
