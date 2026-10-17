// Package fetch is the HTTP client the bluefin-host helpers use where the
// shell helpers ran curl: http and https only, no redirects followed,
// connect and total timeouts, an optional size limit, and curl's --retry
// (timeouts, DNS failures and HTTP 408/429/500/502/503/504/522/524, waiting
// 1 s, then 2 s, or what Retry-After says). Failures carry curl's exit code
// for the same failure, which the helpers' log lines report.
package fetch

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Client is one curl invocation's options.
type Client struct {
	ConnectTimeout time.Duration
	MaxTime        time.Duration
	// MaxSize bounds the body (curl --max-filesize); 0 is no limit.
	MaxSize int64
	Retries int
	// Fail makes HTTP >= 400 an error, exit code 22 (curl -f).
	Fail bool
	// Sleep waits between attempts; nil is time.Sleep.
	Sleep func(time.Duration)
	// Transport replaces the default transport (tests).
	Transport http.RoundTripper
}

// Request is one request; URL may carry user:password@ as curl accepts it.
type Request struct {
	Method string
	URL    string
	Header http.Header
	Body   []byte
}

// Response is the answer of the last attempt.
type Response struct {
	Code int
	Body []byte
}

// Error is a failed transfer with curl's exit code for it.
type Error struct {
	Code int
	Err  error
}

func (e *Error) Error() string { return fmt.Sprintf("(%d) %v", e.Code, e.Err) }

// curl exit codes.
const (
	unsupportedProtocol = 1
	malformedURL        = 3
	couldNotResolve     = 6
	couldNotConnect     = 7
	httpError           = 22
	timedOut            = 28
	sslConnect          = 35
	emptyReply          = 52
	recvError           = 56
	peerVerification    = 60
	fileSize            = 63
)

// Do sends req, retrying like curl --retry. It returns the last response,
// or an *Error when no attempt got one (or, with Fail, the last one was an
// HTTP error; the response is then returned too).
func (c *Client) Do(req Request) (*Response, error) {
	target, user, pass, err := splitURL(req.URL)
	if err != nil {
		return nil, err
	}
	client := &http.Client{
		Transport: c.transport(),
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	wait := time.Second
	for attempt := 0; ; attempt++ {
		resp, after, err := c.once(client, req, target, user, pass)
		retry := false
		var ferr *Error
		if errors.As(err, &ferr) && (ferr.Code == timedOut || ferr.Code == couldNotResolve) {
			retry = true
			after = 0
		} else if resp != nil && transient(resp.Code) {
			retry = true
		}
		if !retry || attempt >= c.Retries {
			return resp, err
		}
		if after == 0 {
			after = wait
			wait *= 2
		}
		c.sleep(after)
	}
}

func (c *Client) sleep(d time.Duration) {
	if c.Sleep != nil {
		c.Sleep(d)
		return
	}
	time.Sleep(d)
}

func (c *Client) transport() http.RoundTripper {
	if c.Transport != nil {
		return c.Transport
	}
	return &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		DialContext:         (&net.Dialer{Timeout: c.ConnectTimeout}).DialContext,
		TLSHandshakeTimeout: c.ConnectTimeout,
		ForceAttemptHTTP2:   true,
	}
}

func transient(code int) bool {
	switch code {
	case 408, 429, 500, 502, 503, 504, 522, 524:
		return true
	}
	return false
}

// once is one attempt; after is the Retry-After delay of the answer.
func (c *Client) once(client *http.Client, req Request, target *url.URL, user, pass *string) (*Response, time.Duration, error) {
	ctx := context.Background()
	if c.MaxTime > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.MaxTime)
		defer cancel()
	}
	method := req.Method
	if method == "" {
		method = http.MethodGet
	}
	var body io.Reader
	if req.Body != nil {
		body = bytes.NewReader(req.Body)
	}
	hreq, err := http.NewRequestWithContext(ctx, method, target.String(), body)
	if err != nil {
		return nil, 0, &Error{malformedURL, err}
	}
	for k, v := range req.Header {
		hreq.Header[k] = v
	}
	if user != nil {
		hreq.SetBasicAuth(*user, *pass)
	}
	hresp, err := client.Do(hreq)
	if err != nil {
		return nil, 0, &Error{classify(err), err}
	}
	defer hresp.Body.Close()
	if c.Fail && hresp.StatusCode >= 400 {
		return &Response{Code: hresp.StatusCode}, retryAfter(hresp, time.Now()),
			&Error{httpError, fmt.Errorf("the requested URL returned error: %d", hresp.StatusCode)}
	}
	if c.MaxSize > 0 && hresp.ContentLength > c.MaxSize {
		return nil, 0, &Error{fileSize, errors.New("maximum file size exceeded")}
	}
	reader := io.Reader(hresp.Body)
	if c.MaxSize > 0 {
		reader = io.LimitReader(hresp.Body, c.MaxSize+1)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, 0, &Error{classify(err), err}
	}
	if c.MaxSize > 0 && int64(len(data)) > c.MaxSize {
		return nil, 0, &Error{fileSize, errors.New("maximum file size exceeded")}
	}
	return &Response{Code: hresp.StatusCode, Body: data}, retryAfter(hresp, time.Now()), nil
}

func retryAfter(resp *http.Response, now time.Time) time.Duration {
	if !transient(resp.StatusCode) {
		return 0
	}
	v := strings.TrimSpace(resp.Header.Get("Retry-After"))
	if v == "" {
		return 0
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		if n <= 0 {
			return 0
		}
		return time.Duration(n) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil && t.After(now) {
		return t.Sub(now).Truncate(time.Second)
	}
	return 0
}

// splitURL checks the scheme and takes the user:password@ part out of the
// URL. curl accepts characters there that net/url refuses (a quote or a
// backslash in a password), so it is split off by hand as curl does: the
// authority runs to the first "/", "?" or "#", the user information to its
// first "@", and a host that still holds an "@" is malformed.
func splitURL(raw string) (*url.URL, *string, *string, error) {
	scheme, rest, ok := strings.Cut(raw, "://")
	if !ok {
		return nil, nil, nil, &Error{malformedURL, fmt.Errorf("no scheme in URL")}
	}
	switch strings.ToLower(scheme) {
	case "http", "https":
	default:
		return nil, nil, nil, &Error{unsupportedProtocol, fmt.Errorf("protocol %q not supported", scheme)}
	}
	end := strings.IndexAny(rest, "/?#")
	if end < 0 {
		end = len(rest)
	}
	authority, tail := rest[:end], rest[end:]
	var user, pass *string
	if info, host, found := strings.Cut(authority, "@"); found {
		if strings.Contains(host, "@") {
			return nil, nil, nil, &Error{malformedURL, fmt.Errorf("bad hostname")}
		}
		u, p, _ := strings.Cut(info, ":")
		u, p = unescape(u), unescape(p)
		user, pass = &u, &p
		authority = host
	}
	target, err := url.Parse(scheme + "://" + authority + tail)
	if err != nil {
		return nil, nil, nil, &Error{malformedURL, err}
	}
	if target.Host == "" {
		return nil, nil, nil, &Error{malformedURL, fmt.Errorf("no host in URL")}
	}
	return target, user, pass, nil
}

func unescape(s string) string {
	if u, err := url.PathUnescape(s); err == nil {
		return u
	}
	return s
}

func classify(err error) int {
	var dns *net.DNSError
	if errors.As(err, &dns) {
		if dns.IsTimeout {
			return timedOut
		}
		return couldNotResolve
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		return timedOut
	}
	var nerr net.Error
	if errors.As(err, &nerr) && nerr.Timeout() {
		return timedOut
	}
	var verify *tls.CertificateVerificationError
	var unknown x509.UnknownAuthorityError
	var host x509.HostnameError
	var invalid x509.CertificateInvalidError
	if errors.As(err, &verify) || errors.As(err, &unknown) || errors.As(err, &host) || errors.As(err, &invalid) {
		return peerVerification
	}
	var record tls.RecordHeaderError
	var alert tls.AlertError
	if errors.As(err, &record) || errors.As(err, &alert) {
		return sslConnect
	}
	var op *net.OpError
	if errors.As(err, &op) && op.Op == "dial" {
		return couldNotConnect
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return emptyReply
	}
	return recvError
}
