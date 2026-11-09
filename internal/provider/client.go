package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"syscall"
	"time"
)

// NewTransport is the HTTP transport for Anthropic and providers.
// headerTimeout bounds the wait for response headers (0 = none).
func NewTransport(headerTimeout time.Duration) *http.Transport {
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: headerTimeout,
	}
}

// Client sends provider requests with a small retry policy.
type Client struct {
	HTTP    *http.Client
	Retries int           // extra attempts after the first
	Backoff time.Duration // wait before the first retry; doubles after
}

// NewClient returns a client that never follows redirects (a redirect
// must not carry the provider key elsewhere).
func NewClient(t http.RoundTripper) *Client {
	return &Client{
		HTTP: &http.Client{
			Transport:     t,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		Retries: 2,
		Backoff: 300 * time.Millisecond,
	}
}

// Do sends the request built by build, retrying only when it is safe and
// quick: the connection was refused or reset before an answer, or the
// provider answered 502/503/504. Timeouts are never retried (with a long
// header timeout a hung provider would block a turn for many minutes and
// get the same POST several times).
func (c *Client) Do(ctx context.Context, build func() (*http.Request, error)) (*http.Response, error) {
	var lastErr error
	wait := c.Backoff
	for attempt := 0; attempt <= c.Retries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
			}
			wait *= 2
		}
		req, err := build()
		if err != nil {
			return nil, err
		}
		resp, err := c.HTTP.Do(req.WithContext(ctx))
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if !retryable(err) {
				return nil, err
			}
			lastErr = err
			continue
		}
		switch resp.StatusCode {
		case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			if attempt < c.Retries {
				io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
				resp.Body.Close()
				lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
				continue
			}
		}
		return resp, nil
	}
	return nil, lastErr
}

// retryable reports connection failures where the provider cannot have
// started work: refused or failed dials, and resets / closed keep-alive
// connections. Timeouts are not retryable.
func retryable(err error) bool {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return false
	}
	var op *net.OpError
	if errors.As(err, &op) && op.Op == "dial" {
		return true
	}
	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}
