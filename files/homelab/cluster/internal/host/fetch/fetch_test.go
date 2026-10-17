package fetch

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type recorder struct {
	mu    sync.Mutex
	paths []string
	auth  []string
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.paths)
}

func (r *recorder) last() (path, auth string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.paths[len(r.paths)-1], r.auth[len(r.auth)-1]
}

func serve(t *testing.T, h func(w http.ResponseWriter, r *http.Request, n int)) (*httptest.Server, *recorder) {
	t.Helper()
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.mu.Lock()
		rec.paths = append(rec.paths, r.URL.Path)
		rec.auth = append(rec.auth, r.Header.Get("Authorization"))
		n := len(rec.paths)
		rec.mu.Unlock()
		h(w, r, n)
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

func client(slept *[]time.Duration) *Client {
	return &Client{ConnectTimeout: 5 * time.Second, MaxTime: 10 * time.Second, Retries: 2, Sleep: func(d time.Duration) { *slept = append(*slept, d) }}
}

func code(err error) int {
	var ferr *Error
	if errors.As(err, &ferr) {
		return ferr.Code
	}
	return -1
}

func TestTransientAnswersAreRetriedTwiceWithBackoff(t *testing.T) {
	srv, rec := serve(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		w.WriteHeader(http.StatusServiceUnavailable)
		io.WriteString(w, "busy")
	})
	var slept []time.Duration
	resp, err := client(&slept).Do(Request{Method: "POST", URL: srv.URL + "/v1/pre-reboot", Body: []byte("{}")})
	if err != nil || resp.Code != 503 || string(resp.Body) != "busy" {
		t.Fatalf("resp=%+v err=%v", resp, err)
	}
	if rec.count() != 3 {
		t.Errorf("attempts = %d; want 3", rec.count())
	}
	if want := []time.Duration{time.Second, 2 * time.Second}; len(slept) != 2 || slept[0] != want[0] || slept[1] != want[1] {
		t.Errorf("slept %v; want %v", slept, want)
	}
}

func TestRetryAfterIsHonoured(t *testing.T) {
	srv, _ := serve(t, func(w http.ResponseWriter, _ *http.Request, n int) {
		if n == 1 {
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		io.WriteString(w, "ok")
	})
	var slept []time.Duration
	resp, err := client(&slept).Do(Request{URL: srv.URL})
	if err != nil || resp.Code != 200 {
		t.Fatalf("resp=%+v err=%v", resp, err)
	}
	if len(slept) != 1 || slept[0] != 7*time.Second {
		t.Errorf("slept %v", slept)
	}
}

func TestPermanentAnswersAndRedirectsAreNotRetriedOrFollowed(t *testing.T) {
	for _, status := range []int{404, 409, 302} {
		srv, rec := serve(t, func(w http.ResponseWriter, r *http.Request, _ int) {
			if status == 302 {
				http.Redirect(w, r, "/elsewhere", status)
				return
			}
			w.WriteHeader(status)
		})
		var slept []time.Duration
		resp, err := client(&slept).Do(Request{URL: srv.URL + "/x"})
		if err != nil || resp.Code != status {
			t.Errorf("%d: resp=%+v err=%v", status, resp, err)
		}
		if p, _ := rec.last(); rec.count() != 1 || p != "/x" || len(slept) != 0 {
			t.Errorf("%d: %d requests, slept %v", status, rec.count(), slept)
		}
	}
}

func TestFailMakesHTTPErrorsCurlExit22(t *testing.T) {
	srv, _ := serve(t, func(w http.ResponseWriter, _ *http.Request, _ int) { w.WriteHeader(404) })
	var slept []time.Duration
	c := client(&slept)
	c.Fail = true
	if _, err := c.Do(Request{URL: srv.URL}); code(err) != 22 {
		t.Errorf("err = %v", err)
	}
}

func TestMaxSize(t *testing.T) {
	srv, _ := serve(t, func(w http.ResponseWriter, r *http.Request, _ int) {
		if r.URL.Path == "/chunked" {
			w.(http.Flusher).Flush()
		}
		io.WriteString(w, strings.Repeat("x", 11))
	})
	var slept []time.Duration
	c := client(&slept)
	c.MaxSize = 10
	for _, p := range []string{"/sized", "/chunked"} {
		if _, err := c.Do(Request{URL: srv.URL + p}); code(err) != 63 {
			t.Errorf("%s: err = %v", p, err)
		}
	}
	c.MaxSize = 11
	if resp, err := c.Do(Request{URL: srv.URL}); err != nil || len(resp.Body) != 11 {
		t.Errorf("at the limit: %v", err)
	}
}

func TestUnreachableServerIsCurlExit7AndNotRetried(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	var slept []time.Duration
	if _, err := client(&slept).Do(Request{URL: "http://" + addr + "/"}); code(err) != 7 {
		t.Errorf("err = %v", err)
	}
	if len(slept) != 0 {
		t.Errorf("connection refused is not retried: %v", slept)
	}
}

func TestTimeoutIsCurlExit28AndRetried(t *testing.T) {
	release := make(chan struct{})
	srv, rec := serve(t, func(_ http.ResponseWriter, _ *http.Request, _ int) { <-release })
	defer close(release)
	var slept []time.Duration
	c := client(&slept)
	c.MaxTime = 50 * time.Millisecond
	if _, err := c.Do(Request{URL: srv.URL}); code(err) != 28 {
		t.Errorf("err = %v", err)
	}
	if rec.count() != 3 || len(slept) != 2 {
		t.Errorf("attempts %d slept %v", rec.count(), slept)
	}
}

func TestCredentialsAreSplitAsCurlDoes(t *testing.T) {
	srv, rec := serve(t, func(_ http.ResponseWriter, _ *http.Request, _ int) {})
	host := strings.TrimPrefix(srv.URL, "http://")
	var slept []time.Duration
	c := client(&slept)
	for raw, user := range map[string]string{
		"http://user:secret@" + host + "/":    "user:secret",
		`http://user:p"a\ss@` + host:          `user:p"a\ss`,
		"http://user:se%40cret@" + host + "/": "user:se@cret",
	} {
		if _, err := c.Do(Request{URL: raw}); err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		_, auth := rec.last()
		r := &http.Request{Header: http.Header{"Authorization": {auth}}}
		u, p, ok := r.BasicAuth()
		if !ok || u+":"+p != user {
			t.Errorf("%s: sent %q:%q", raw, u, p)
		}
	}
	if _, err := c.Do(Request{URL: "http://user:se@cret@" + host + "/"}); code(err) != 3 {
		t.Errorf("an @ left in the host: %v", err)
	}
}

func TestOnlyHTTPAndHTTPS(t *testing.T) {
	var slept []time.Duration
	for raw, want := range map[string]int{
		"file:///etc/passwd":  1,
		"ftp://example.org/x": 1,
		"example.org/x":       3,
		"http:///x":           3,
	} {
		if _, err := client(&slept).Do(Request{URL: raw}); code(err) != want {
			t.Errorf("%s: %v; want curl exit %d", raw, err, want)
		}
	}
}
