// Package update tells a person, at most once a day, that a newer headroom
// exists. It prints one notice and does nothing else.
//
// It earns its place because of what this tool is. A capacity ceiling is a
// claim about a provider, and providers move theirs constantly: a quota is
// raised, an instance family is added, a limit page is rewritten, a terraform
// provider renames the attribute a rule reads. The catalog inside an old binary
// answers with the old number, in the same confident format as the right one.
// That is the worst way for this tool in particular to be wrong, because a
// wrong ceiling is indistinguishable from a right one at the point of use. So
// the notice is not a convenience feature, it is a correctness signal, and it
// says so on the terminal.
//
// What this package will not do, and what each refusal is protecting:
//
//   - It sends nothing about the user. One GET to a public endpoint. No query
//     string, no version in the User-Agent, no machine id, no plan, nothing.
//     The request is indistinguishable from any other anonymous read of a
//     public release page. "The CLI reports nothing" stays true.
//   - It never downloads, replaces or runs anything. It prints a sentence. A
//     binary that can rewrite itself is a supply chain with one link and no
//     review, and this project ships Sigstore signed artifacts precisely so
//     that installing is a decision somebody makes.
//   - It never runs unless a person is watching. Not in CI, not through a pipe,
//     not from cron. See Wanted.
//   - It never fails, delays or changes a run. Every error path here ends in
//     silence, the check rides alongside the analysis rather than in front of
//     it, and Notify abandons it rather than hold the process open.
//   - It never writes to stdout. The report and the redacted payload own stdout
//     byte for byte, and the --dry-run audit claim depends on that staying
//     exactly as true as it is today.
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/headroom-project/headroom/internal/tty"
)

const (
	repo = "headroom-project/headroom"

	// releasesAPI excludes drafts and prereleases server side. That is the
	// reason this endpoint is used rather than the tag list: the ordering
	// question "is a release candidate newer than a release" is one this
	// package should never have to answer from a guess.
	releasesAPI = "https://api.github.com/repos/" + repo + "/releases/latest"

	// releasePage is built here from a tag that parseSemver accepted, never
	// taken from the response. The API returns an html_url and it is ignored on
	// purpose: a URL chosen by a server is a string chosen by a server, and
	// this one gets printed to a terminal.
	releasePage = "https://github.com/" + repo + "/releases/tag/"

	// userAgent carries no version, deliberately. GitHub rejects a request with
	// no User-Agent at all, so one is required; making it name the build would
	// turn a request that carries nothing about the user into a request that
	// carries something about the user.
	userAgent = "headroom"

	cacheFile = "update-check.json"

	// maxBody caps the response. A release body is prose written by a human and
	// can be long, but it is never a megabyte, and this process should not be
	// able to be made to read one.
	maxBody = 1 << 20
)

// DefaultTimeout bounds the entire check, measured from Start rather than from
// the request. Whatever the analysis spends is spent out of this budget, so on
// a real plan the marginal cost is usually nothing at all.
const DefaultTimeout = 2 * time.Second

const (
	// freshFor is how long a successful answer stands. Once a day is enough for
	// a tool nobody releases hourly, and it keeps a laptop that runs headroom
	// forty times in an afternoon down to one request.
	freshFor = 24 * time.Hour

	// retryAfter is how long a failed answer stands. Shorter, because a failure
	// is usually the shared address rate limit or a flaky network rather than a
	// fact, but not zero, because retrying every invocation is how a best effort
	// check turns into a request per run.
	retryAfter = 4 * time.Hour
)

// client makes the one request.
//
// A redirect is not followed, for the same reason upload does not follow one.
// This is a single request with a stated budget, and a 3xx here simply means no
// notice this run, which is a cost of nothing.
var client = &http.Client{
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// Config is everything the check needs. The zero value of each field means
// "the default", so a caller states only what it wants to be different, which
// in the CLI is Current and Enabled.
type Config struct {
	// Current is the running version. Anything that is not a semantic version
	// disables the check, which includes "dev", the value a build from source
	// carries. There is no honest suggestion to make to a binary that cannot
	// say where it sits on the number line.
	Current string

	// Enabled is the caller's decision, normally Wanted(stderr, flag).
	Enabled bool

	// APIURL, CacheDir, Timeout and Now exist for tests. Nothing in the CLI
	// sets them, and there is no flag or environment variable that reaches
	// them: an override for the update endpoint would be a way to point somebody
	// else's binary at somebody else's server.
	APIURL   string
	CacheDir string
	Timeout  time.Duration
	Now      func() time.Time
}

// Probe is an in flight check. A nil *Probe is the disabled check and every
// method on it is a no-op, so a caller never needs a branch.
type Probe struct {
	current    semver
	currentRaw string
	result     chan entry
	expires    *time.Timer
}

// entry is both the answer and the cache file. Latest is empty when the last
// attempt failed, which is what makes retryAfter distinguishable from freshFor.
type entry struct {
	CheckedAt time.Time `json:"checked_at"`
	Latest    string    `json:"latest_version"`
}

// Wanted decides whether the check runs at all, in this order:
//
//  1. the --no-update-check flag, because an explicit flag beats an inherited
//     environment
//  2. HEADROOM_NO_UPDATE_CHECK, set to anything at all, including empty
//  3. CI, set to anything but 0 or false
//  4. w is a character device, which is the question that actually matters: is
//     there a person here to read a sentence
//
// Steps 3 and 4 overlap, and that is intended. A pipeline is the case this must
// never bother and the case where an unexpected outbound request is least
// welcome, so it is worth two independent answers rather than one.
func Wanted(w io.Writer, noUpdateCheck bool) bool {
	if noUpdateCheck {
		return false
	}
	if _, set := os.LookupEnv("HEADROOM_NO_UPDATE_CHECK"); set {
		return false
	}
	if v, set := os.LookupEnv("CI"); set && v != "" && v != "0" && !strings.EqualFold(v, "false") {
		return false
	}
	return tty.Is(w)
}

// Start begins the check and returns immediately. It opens a socket only when
// the cached answer has expired, and never blocks the caller either way.
func Start(cfg Config) *Probe {
	if !cfg.Enabled {
		return nil
	}
	current, err := parseSemver(cfg.Current)
	if err != nil {
		return nil
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.APIURL == "" {
		cfg.APIURL = releasesAPI
	}
	dir := cfg.CacheDir
	if dir == "" {
		dir = defaultCacheDir()
	}

	p := &Probe{
		current:    current,
		currentRaw: cfg.Current,
		result:     make(chan entry, 1),
		// Started on the real clock even when Now is injected. Now decides how
		// old a cache file is; this decides how long a human waits, and the two
		// must not be the same knob.
		expires: time.NewTimer(cfg.Timeout),
	}

	if e, ok := readCache(dir); ok && fresh(e, cfg.Now()) {
		p.result <- e
		return p
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
		defer cancel()
		var e entry
		if latest, err := fetch(ctx, cfg.APIURL); err == nil {
			e.Latest = latest
		}
		e.CheckedAt = cfg.Now()
		// Written before the send, so an answer that arrives after Notify gave
		// up is still worth having on the next run.
		writeCache(dir, e)
		p.result <- e
	}()
	return p
}

// Notify prints the notice, or prints nothing. Silence is the answer to every
// case except one: a release exists that is strictly newer than this build.
func (p *Probe) Notify(w io.Writer) {
	if p == nil {
		return
	}
	var e entry
	select {
	case e = <-p.result:
		p.expires.Stop()
	case <-p.expires.C:
		// The budget ran out. The analysis is already printed and the exit code
		// is already decided, and neither is going to be held open by a socket
		// whose only job was to mention a version number.
		return
	}
	if e.Latest == "" {
		return
	}
	// Two locks on the same door, and they are deliberate rather than left over.
	//
	// The first is this parse. e.Latest normally arrives from fetch, which has
	// already vetted it, but it can also come off a cache file on disk, and a
	// file is not evidence of anything.
	//
	// The second is compare. A tag that fails to parse yields the zero version,
	// and the zero version sits below every release this project has ever cut,
	// so it can never reach the print below. Either lock alone holds. Do not
	// remove them both on the grounds that one of them is unreachable, because
	// which one is unreachable depends on the other.
	latest, err := parseSemver(e.Latest)
	if err != nil {
		return
	}
	if compare(latest, p.current) <= 0 {
		return
	}

	// Every byte below is either a constant in this file or a string that
	// parseSemver accepted, which is what makes printing it to a terminal safe.
	fmt.Fprintf(w, "\nheadroom %s is available, and this is %s.\n", e.Latest, p.currentRaw)
	fmt.Fprintf(w, "Provider limits move, so an older catalog answers with an older ceiling.\n")
	fmt.Fprintf(w, "  %s%s\n", releasePage, e.Latest)
	fmt.Fprintf(w, "  silence this with --no-update-check, or HEADROOM_NO_UPDATE_CHECK=1\n")
}

// fetch asks once and returns a release tag that parseSemver has accepted.
func fetch(ctx context.Context, apiURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// 403 and 429 are the unauthenticated rate limit, which is the ordinary
		// answer from a shared address, and nobody needs to read about it.
		return "", fmt.Errorf("GET returned %d", resp.StatusCode)
	}

	var body struct {
		Tag        string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(&body); err != nil {
		return "", err
	}
	if body.Draft || body.Prerelease {
		// This endpoint already excludes both. Checked anyway, because the cost
		// is one line and the failure it prevents is telling a stranger to
		// install a draft.
		return "", errors.New("latest release is a draft or a prerelease")
	}
	if _, err := parseSemver(body.Tag); err != nil {
		return "", err
	}
	return body.Tag, nil
}

// fresh reports whether a cached answer still stands.
func fresh(e entry, now time.Time) bool {
	age := now.Sub(e.CheckedAt)
	// A clock that moved backwards, or a cache file stamped in the future,
	// reads as stale. The alternative is a cache that is fresh forever.
	if age < 0 {
		return false
	}
	if e.Latest == "" {
		return age < retryAfter
	}
	return age < freshFor
}

func defaultCacheDir() string {
	base, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(base, "headroom")
}

func readCache(dir string) (entry, bool) {
	if dir == "" {
		return entry{}, false
	}
	raw, err := os.ReadFile(filepath.Join(dir, cacheFile))
	if err != nil {
		return entry{}, false
	}
	var e entry
	if err := json.Unmarshal(raw, &e); err != nil {
		return entry{}, false
	}
	return e, true
}

// writeCache is best effort in every direction. A read only home, a full disk
// and a half finished write all end the same way, which is that the next run
// asks again.
//
// It writes in place rather than through a temporary file and a rename. A
// process that exits while this goroutine is mid write can leave half a JSON
// object behind, and half a JSON object fails to parse and reads as no cache,
// which heals itself. A temporary file would instead leave one stray file per
// interrupted run sitting in somebody's cache directory forever.
func writeCache(dir string, e entry) {
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, cacheFile), raw, 0o600)
}
