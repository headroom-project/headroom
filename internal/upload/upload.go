// Package upload sends a redacted payload to the headroom API.
//
// This is the only place in the module that opens a socket. It is deliberately
// small: one request, one attempt, one stated timeout, and no retry. A capacity
// tool that hammers an API on failure turns somebody else's brief outage into
// their own, and a pipeline that has already printed its report loses nothing by
// being told to run again.
//
// The bytes handed to Post are the bytes the caller printed for --dry-run.
// Nothing here reshapes, re-encodes or adds to them, because the moment this
// package can change the payload the audit story stops being true.
package upload

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultBaseURL is the production API, and the value --api-url falls back to.
const DefaultBaseURL = "https://api.headroomcli.com"

// Path is the ingest route. One route, spelled once.
const Path = "/v1/reports"

// DefaultTimeout bounds the whole attempt: connect, write, read. It is stated
// rather than inherited so that a hung server costs a pipeline a known amount of
// time instead of the default of forever.
const DefaultTimeout = 30 * time.Second

// maxErrorBody caps how much of a failure response is read. An error message is
// for a human, and a server that answers an upload with a megabyte of HTML
// should not get to put a megabyte of HTML in somebody's CI log.
const maxErrorBody = 8 << 10

// Response is the 201 body: the report the server stored.
type Response struct {
	ID            string `json:"id"`
	FindingsCount int    `json:"findings_count"`
	CreatedAt     string `json:"created_at"`
}

// apiError is the error envelope the API uses everywhere. The code is stable and
// worth naming in a message; the message itself is prose and may change.
type apiError struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// Client posts one report. The zero value is not usable: NewClient checks the
// things that are cheaper to reject before a socket is opened than after.
type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

// NewClient validates the destination up front.
//
// Cleartext HTTP to anything but loopback is refused. The API key is a bearer
// token, and a bearer token in cleartext is a bearer token that belongs to
// whoever is on the path. Loopback stays allowed because that is where a test
// server and a local proxy live, and neither leaves the machine.
func NewClient(baseURL, apiKey string, timeout time.Duration) (*Client, error) {
	if apiKey == "" {
		return nil, errors.New("no API key")
	}
	base := strings.TrimSuffix(baseURL, "/")
	u, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("--api-url %q is not a URL: %w", baseURL, err)
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !isLoopback(u.Hostname()) {
			return nil, fmt.Errorf("--api-url %q is cleartext http, which would put the API key on the wire in the clear (use https, or a loopback address for local testing)", base)
		}
	default:
		return nil, fmt.Errorf("--api-url %q must be https (got scheme %q)", base, u.Scheme)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("--api-url %q has no host", base)
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &Client{
		baseURL: base,
		apiKey:  apiKey,
		http: &http.Client{
			Timeout: timeout,
			// A redirect is not followed. Go already strips the Authorization
			// header when a redirect crosses hosts, but "one attempt" is easier
			// to reason about than "one attempt plus wherever the server points
			// us", and an ingest endpoint has no business redirecting.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

// Post sends body verbatim and returns what the server stored.
//
// Every error returned from here is scrubbed of the API key before it leaves,
// including whatever the server chose to put in its response body. A server that
// echoes the Authorization header back into an error message must not be able to
// write the key into a CI log through us.
func (c *Client) Post(ctx context.Context, body []byte) (*Response, error) {
	res, err := c.post(ctx, body)
	if err != nil {
		return nil, errors.New(c.scrub(err.Error()))
	}
	return res, nil
}

func (c *Client) post(ctx context.Context, body []byte) (*Response, error) {
	endpoint := c.baseURL + Path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.ContentLength = int64(len(body))

	resp, err := c.http.Do(req)
	if err != nil {
		// One attempt, so say so: the reader needs to know nothing is being
		// retried behind their back.
		return nil, fmt.Errorf("POST %s failed after one attempt: %w", safeURL(endpoint), unwrapURLError(err))
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("POST %s returned %d%s", safeURL(endpoint), resp.StatusCode, describe(resp))
	}

	var out Response
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxErrorBody)).Decode(&out); err != nil {
		return nil, fmt.Errorf("POST %s returned 201 with a body this client cannot read: %w", safeURL(endpoint), err)
	}
	return &out, nil
}

// describe turns a failure response into the shortest useful suffix: the stable
// error code when the body carries one, and nothing at all when it does not.
func describe(resp *http.Response) string {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	if err != nil || len(bytes.TrimSpace(raw)) == 0 {
		return ""
	}
	var env apiError
	if err := json.Unmarshal(raw, &env); err == nil && env.Error.Code != "" {
		if env.Error.Message != "" {
			return fmt.Sprintf(" (%s: %s)", env.Error.Code, oneLine(env.Error.Message))
		}
		return fmt.Sprintf(" (%s)", env.Error.Code)
	}
	return ""
}

// scrub removes the API key from anything on its way to a terminal. The key is
// never printed on purpose, and this is the guard for the times it would arrive
// by accident: a server echoing a header, a proxy quoting a request line.
func (c *Client) scrub(s string) string {
	if c.apiKey == "" {
		return s
	}
	return strings.ReplaceAll(s, c.apiKey, "[redacted]")
}

// safeURL drops any userinfo before a URL is quoted in an error. Nobody should
// put a credential in --api-url, and if they do it stops here.
func safeURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	u.User = nil
	return u.String()
}

// unwrapURLError strips the *url.Error wrapper, which repeats the method and the
// URL that the message already names.
func unwrapURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	return strings.TrimSpace(s)
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
