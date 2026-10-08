package main

import (
	"net/http"
	"testing"
	"time"
)

func TestShutdownRequiresTokenAndDirectLocalCall(t *testing.T) {
	ts, srv := newTestServer(t, "", t.TempDir())
	srv.controlToken = "secret"
	srv.shutdownRequested = make(chan struct{})

	post := func(token, forwardedFor string) int {
		request, _ := http.NewRequest(http.MethodPost, ts.URL+controlPath, nil)
		request.Header.Set(controlHeader, token)
		if forwardedFor != "" {
			request.Header.Set("X-Forwarded-For", forwardedFor)
		}
		resp, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	if code := post("wrong", ""); code != http.StatusForbidden {
		t.Fatalf("wrong token: got %d", code)
	}
	if code := post("secret", "203.0.113.7"); code != http.StatusNotFound {
		t.Fatalf("request through a reverse proxy: got %d", code)
	}
	select {
	case <-srv.shutdownRequested:
		t.Fatal("refused requests must not stop the server")
	default:
	}

	if code := post("secret", ""); code != http.StatusAccepted {
		t.Fatalf("valid request: got %d", code)
	}
	select {
	case <-srv.shutdownRequested:
	case <-time.After(time.Second):
		t.Fatal("shutdown was not requested")
	}
	if code := post("secret", ""); code != http.StatusAccepted {
		t.Fatalf("a second request must not panic: got %d", code)
	}
}

func TestShutdownDisabledWithoutToken(t *testing.T) {
	ts, _ := newTestServer(t, "", t.TempDir())
	resp, err := http.Post(ts.URL+controlPath, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("got %d", resp.StatusCode)
	}
}
