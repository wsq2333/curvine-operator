package v054

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"time"
	"unicode/utf8"
)

var (
	ErrUnavailable      = errors.New("Curvine observation endpoint unavailable")
	ErrInvalidResponse  = errors.New("Curvine observation response violates the release contract")
	ErrIdentityMismatch = errors.New("Curvine observation identity mismatch")
)

const maxBodyBytes = 4 << 20

// The client contacts only generated Master pod DNS endpoints. It does not
// follow redirects, use ambient proxies or call config/mutating endpoints.
func newClient(transport http.RoundTripper) *http.Client {
	if transport == nil {
		transport = &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 2 * time.Second}).DialContext, ResponseHeaderTimeout: 2 * time.Second, IdleConnTimeout: 30 * time.Second, MaxIdleConns: 10, MaxIdleConnsPerHost: 2, DisableCompression: true}
	}
	return &http.Client{Transport: transport, Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
func (o *Observer) get(ctx context.Context, host, path string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+net.JoinHostPort(host, "9000")+path, nil)
	if err != nil {
		return ErrInvalidResponse
	}
	req.Header.Set("Accept", "application/json")
	response, err := o.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return ErrUnavailable
	}
	// Unknown routes can return the SPA index with HTTP 200. Never treat that
	// HTML fallback as an empty or healthy response.
	typ, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || typ != "application/json" {
		return ErrInvalidResponse
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxBodyBytes+1))
	if err != nil {
		return ErrUnavailable
	}
	if len(data) > maxBodyBytes || !utf8.Valid(data) || !validJSON(data) {
		return ErrInvalidResponse
	}
	if err := json.Unmarshal(data, dst); err != nil {
		return ErrInvalidResponse
	}
	return nil
}

// Reject duplicate keys, including differently escaped spellings, while
// allowing unknown additive upstream fields. Typed decoding validates values.
func validJSON(data []byte) bool {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var value func(int) bool
	value = func(depth int) bool {
		if depth > 64 {
			return false
		}
		token, err := d.Token()
		if err != nil {
			return false
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return true
		}
		switch delim {
		case '{':
			keys := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return false
				}
				name, ok := key.(string)
				if !ok || keys[name] {
					return false
				}
				keys[name] = true
				if !value(depth + 1) {
					return false
				}
			}
			end, err := d.Token()
			return err == nil && end == json.Delim('}')
		case '[':
			for d.More() {
				if !value(depth + 1) {
					return false
				}
			}
			end, err := d.Token()
			return err == nil && end == json.Delim(']')
		}
		return false
	}
	if !value(0) {
		return false
	}
	_, err := d.Token()
	return err == io.EOF
}
