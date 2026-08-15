package upload

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Nothing in this file touches a real network. Every server here is an
// httptest.Server on loopback, which is also why NewClient allows cleartext to
// loopback and to nothing else.

const testKey = "hr_live_0123456789abcdef"

func newTestClient(t *testing.T, base string) *Client {
	t.Helper()
	c, err := NewClient(base, testKey, 5*time.Second)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

func TestPostSendsTheBodyVerbatimWithTheRightHeaders(t *testing.T) {
	body := []byte(`{"schema_version":"1","nodes":[]}` + "\n")

	var got struct {
		method, path, auth, contentType string
		body                            []byte
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.method, got.path = r.Method, r.URL.Path
		got.auth = r.Header.Get("Authorization")
		got.contentType = r.Header.Get("Content-Type")
		got.body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(Response{ID: "rep_1", FindingsCount: 3, CreatedAt: "2026-08-15T00:00:00Z"})
	}))
	defer srv.Close()

	res, err := newTestClient(t, srv.URL).Post(context.Background(), body)
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	if got.method != http.MethodPost || got.path != Path {
		t.Errorf("request = %s %s, want POST %s", got.method, got.path, Path)
	}
	if got.auth != "Bearer "+testKey {
		t.Errorf("Authorization = %q, want a bearer token", got.auth)
	}
	if got.contentType != "application/json" {
		t.Errorf("Content-Type = %q", got.contentType)
	}
	if string(got.body) != string(body) {
		t.Errorf("the server received %q, not the bytes it was given (%q)", got.body, body)
	}
	if res.ID != "rep_1" || res.FindingsCount != 3 {
		t.Errorf("response = %+v", res)
	}
}

// A trailing slash on --api-url must not produce //v1/reports, which some
// routers answer with a 404 that then looks like a missing endpoint.
func TestBaseURLTrailingSlashIsNormalised(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"x"}`))
	}))
	defer srv.Close()

	if _, err := newTestClient(t, srv.URL+"/").Post(context.Background(), []byte("{}")); err != nil {
		t.Fatalf("Post: %v", err)
	}
	if path != Path {
		t.Errorf("path = %q, want %q", path, Path)
	}
}

// The error a human reads has to name the status and, when the server says one,
// the stable machine-readable code. "upload failed" on its own sends somebody to
// read source they do not have.
func TestErrorNamesTheStatusAndTheServerCode(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   []string
	}{
		{"unauthenticated", 401, `{"error":{"code":"unauthenticated","message":"bad key"}}`, []string{"401", "unauthenticated", "bad key"}},
		{"invalid body", 400, `{"error":{"code":"invalid_body","message":"nodes: required"}}`, []string{"400", "invalid_body"}},
		{"rate limited", 429, `{"error":{"code":"rate_limited"}}`, []string{"429", "rate_limited"}},
		{"no envelope", 502, `<html>bad gateway</html>`, []string{"502"}},
		{"empty body", 500, ``, []string{"500"}},
		// 200 is not 201. The contract says the server creates a report and says
		// so; anything else is a proxy or a captive portal answering for it.
		{"almost created", 200, `{"id":"rep_1"}`, []string{"200"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()

			_, err := newTestClient(t, srv.URL).Post(context.Background(), []byte("{}"))
			if err == nil {
				t.Fatal("no error for a non-201 response")
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not mention %q", err, want)
				}
			}
		})
	}
}

// One attempt. A capacity tool that retries on failure turns a brief outage at
// the API into a self-inflicted one, multiplied by every pipeline running it.
func TestFailureIsAttemptedExactlyOnce(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	if _, err := newTestClient(t, srv.URL).Post(context.Background(), []byte("{}")); err == nil {
		t.Fatal("no error for a 500")
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Errorf("the server saw %d requests, want exactly 1", n)
	}
}

// A redirect is not followed, so an ingest endpoint cannot be talked into
// forwarding a request somewhere else, and "one attempt" stays literally true.
func TestARedirectIsNotFollowed(t *testing.T) {
	var elsewhere int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&elsewhere, 1)
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"id":"rep_1"}`)
	}))
	defer target.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+Path, http.StatusFound)
	}))
	defer srv.Close()

	_, err := newTestClient(t, srv.URL).Post(context.Background(), []byte("{}"))
	if err == nil {
		t.Fatal("a redirect was treated as success")
	}
	if !strings.Contains(err.Error(), "302") {
		t.Errorf("error %q does not name the redirect status", err)
	}
	if n := atomic.LoadInt32(&elsewhere); n != 0 {
		t.Errorf("the redirect target was called %d times; the payload followed the redirect", n)
	}
}

// The timeout is stated and it is enforced. A hung API must cost a pipeline a
// known amount of time, not the rest of the build.
func TestTheTimeoutIsEnforced(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer func() {
		close(release)
		srv.Close()
	}()

	c, err := NewClient(srv.URL, testKey, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	start := time.Now()
	if _, err := c.Post(context.Background(), []byte("{}")); err == nil {
		t.Fatal("a hung server did not produce an error")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("the request took %s, so the timeout is not doing anything", elapsed)
	}
}

// The key is never printed on purpose. This is the guard for the accident: a
// server, or something pretending to be one, echoing the Authorization header
// into a message that then lands in a CI log a whole team can read.
func TestTheAPIKeyIsScrubbedFromEveryError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"code":"unauthenticated","message":"the key `+testKey+` is not valid"}}`)
	}))
	defer srv.Close()

	_, err := newTestClient(t, srv.URL).Post(context.Background(), []byte("{}"))
	if err == nil {
		t.Fatal("no error for a 401")
	}
	if strings.Contains(err.Error(), testKey) {
		t.Fatalf("the API key is in the error text: %q", err)
	}
	if !strings.Contains(err.Error(), "[redacted]") {
		t.Errorf("the echoed key was dropped rather than marked: %q", err)
	}
}

// A bearer token in cleartext belongs to whoever is on the path. Loopback is the
// exception, because that is a test server or a local proxy and it never leaves
// the machine.
func TestCleartextIsRefusedExceptOnLoopback(t *testing.T) {
	refused := []string{
		"http://api.headroomcli.com",
		"http://10.0.0.1:8080",
		"ftp://api.headroomcli.com",
		"api.headroomcli.com",
	}
	for _, base := range refused {
		if _, err := NewClient(base, testKey, time.Second); err == nil {
			t.Errorf("NewClient(%q) was accepted", base)
		}
	}
	allowed := []string{
		"https://api.headroomcli.com",
		"http://127.0.0.1:8787",
		"http://localhost:8787",
		"http://[::1]:8787",
	}
	for _, base := range allowed {
		if _, err := NewClient(base, testKey, time.Second); err != nil {
			t.Errorf("NewClient(%q) was refused: %v", base, err)
		}
	}
}

func TestNewClientRefusesAnEmptyKey(t *testing.T) {
	if _, err := NewClient("https://api.headroomcli.com", "", time.Second); err == nil {
		t.Fatal("a client was built with no API key")
	}
}

// Nobody should put a credential in a URL, and if somebody does it must not come
// back out in an error message.
func TestUserinfoIsStrippedFromAnErrorURL(t *testing.T) {
	c, err := NewClient("https://user:hunter2@127.0.0.1:1/", testKey, 20*time.Millisecond)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	_, err = c.Post(context.Background(), []byte("{}"))
	if err == nil {
		t.Fatal("connecting to a closed port succeeded")
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Errorf("the error carries the userinfo: %q", err)
	}
}

// A server answering an upload with a very large body must not be able to put
// all of it in somebody's terminal.
func TestAnEnormousErrorBodyIsBounded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, strings.Repeat("x", 1<<20))
	}))
	defer srv.Close()

	_, err := newTestClient(t, srv.URL).Post(context.Background(), []byte("{}"))
	if err == nil {
		t.Fatal("no error for a 500")
	}
	if len(err.Error()) > 1024 {
		t.Errorf("the error is %d bytes long", len(err.Error()))
	}
}
