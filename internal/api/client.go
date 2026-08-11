// Package api is a thin HTTP client for the Cloudflare v4 API. It knows the
// response envelope, pagination, and how to dump a request instead of
// sending it (--dry-run).
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultBaseURL is the Cloudflare v4 API root.
const DefaultBaseURL = "https://api.cloudflare.com/client/v4"

const maxPages = 1000

type Client struct {
	BaseURL    string
	Token      string
	UserAgent  string
	HTTPClient *http.Client
}

// New builds a client. baseURL may be empty for the default.
func New(baseURL, token, version string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		Token:      token,
		UserAgent:  "cf-cli/" + version,
		HTTPClient: &http.Client{Timeout: 60 * time.Second},
	}
}

// Request is a fully-resolved API request: Path has no {placeholders} left.
// ContentType applies when Body is set and defaults to application/json.
type Request struct {
	Method      string
	Path        string
	Query       url.Values
	Body        []byte
	ContentType string
}

func (r Request) contentType() string {
	if r.ContentType != "" {
		return r.ContentType
	}
	return "application/json"
}

type Message struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type ResultInfo struct {
	Page       int    `json:"page"`
	PerPage    int    `json:"per_page"`
	TotalPages int    `json:"total_pages"`
	Count      int    `json:"count"`
	TotalCount int    `json:"total_count"`
	Cursor     string `json:"cursor"`
	Cursors    struct {
		After  string `json:"after"`
		Before string `json:"before"`
	} `json:"cursors"`
}

// Envelope is the standard Cloudflare v4 response wrapper. Responses that
// don't use the wrapper (raw exports etc.) are normalized into one.
type Envelope struct {
	Success    bool            `json:"success"`
	Errors     []Message       `json:"errors,omitempty"`
	Messages   []Message       `json:"messages,omitempty"`
	Result     json.RawMessage `json:"result"`
	ResultInfo *ResultInfo     `json:"result_info,omitempty"`
}

type APIError struct {
	StatusCode int
	Errors     []Message
	RawBody    string
}

func (e *APIError) Error() string {
	if len(e.Errors) == 0 {
		body := e.RawBody
		if len(body) > 300 {
			body = body[:300] + "..."
		}
		return fmt.Sprintf("API request failed with HTTP %d: %s", e.StatusCode, body)
	}
	parts := make([]string, 0, len(e.Errors))
	for _, m := range e.Errors {
		parts = append(parts, fmt.Sprintf("%d: %s", m.Code, m.Message))
	}
	return fmt.Sprintf("API error (HTTP %d): %s", e.StatusCode, strings.Join(parts, "; "))
}

func (c *Client) buildURL(r Request) (string, error) {
	p := r.Path
	rawQuery := ""
	if i := strings.IndexByte(p, '?'); i >= 0 {
		rawQuery = p[i+1:]
		p = p[:i]
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	q := url.Values{}
	if rawQuery != "" {
		parsed, err := url.ParseQuery(rawQuery)
		if err != nil {
			return "", fmt.Errorf("invalid query string in path: %w", err)
		}
		for k, vs := range parsed {
			for _, v := range vs {
				q.Add(k, v)
			}
		}
	}
	for k, vs := range r.Query {
		for _, v := range vs {
			q.Add(k, v)
		}
	}
	u := c.BaseURL + p
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return u, nil
}

// RequestDump is the --dry-run representation of a request. The token is
// always redacted.
type RequestDump struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Body    json.RawMessage   `json:"body,omitempty"`
}

// Dump renders the request that Do would send, without sending it.
func (c *Client) Dump(r Request) (*RequestDump, error) {
	u, err := c.buildURL(r)
	if err != nil {
		return nil, err
	}
	h := map[string]string{
		"Accept":     "application/json",
		"User-Agent": c.UserAgent,
	}
	if c.Token != "" {
		h["Authorization"] = "Bearer ********"
	}
	if len(r.Body) > 0 {
		h["Content-Type"] = r.contentType()
	}
	d := &RequestDump{Method: r.Method, URL: u, Headers: h}
	if len(r.Body) > 0 {
		if r.contentType() == "application/json" && json.Valid(r.Body) {
			d.Body = r.Body
		} else {
			quoted, _ := json.Marshal(string(r.Body))
			d.Body = quoted
		}
	}
	return d, nil
}

// Do sends the request and returns the parsed envelope. A non-2xx status or
// success=false yields an *APIError (with the envelope still returned when
// available).
func (c *Client) Do(ctx context.Context, r Request) (*Envelope, error) {
	u, err := c.buildURL(r)
	if err != nil {
		return nil, err
	}
	var body io.Reader
	if len(r.Body) > 0 {
		body = bytes.NewReader(r.Body)
	}
	req, err := http.NewRequestWithContext(ctx, r.Method, u, body)
	if err != nil {
		return nil, err
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if len(r.Body) > 0 {
		req.Header.Set("Content-Type", r.contentType())
	}
	req.Header.Set("User-Agent", c.UserAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 100<<20))
	if err != nil {
		return nil, err
	}
	env := parseEnvelope(resp.StatusCode, data)
	if resp.StatusCode >= 400 || !env.Success {
		return env, &APIError{StatusCode: resp.StatusCode, Errors: env.Errors, RawBody: string(data)}
	}
	return env, nil
}

// RawResponse is the result of DoRaw: the body verbatim, no envelope
// parsing. For endpoints that return raw bytes (KV values, R2 objects,
// BIND exports).
type RawResponse struct {
	StatusCode  int
	ContentType string
	Body        []byte
}

// DoRaw sends the request and returns the response body untouched. Error
// responses (>= 400) are parsed as envelopes when possible so *APIError
// still carries Cloudflare's error messages.
func (c *Client) DoRaw(ctx context.Context, r Request) (*RawResponse, error) {
	u, err := c.buildURL(r)
	if err != nil {
		return nil, err
	}
	var body io.Reader
	if len(r.Body) > 0 {
		body = bytes.NewReader(r.Body)
	}
	req, err := http.NewRequestWithContext(ctx, r.Method, u, body)
	if err != nil {
		return nil, err
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if len(r.Body) > 0 {
		req.Header.Set("Content-Type", r.contentType())
	}
	req.Header.Set("User-Agent", c.UserAgent)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 100<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		env := parseEnvelope(resp.StatusCode, data)
		return nil, &APIError{StatusCode: resp.StatusCode, Errors: env.Errors, RawBody: string(data)}
	}
	return &RawResponse{
		StatusCode:  resp.StatusCode,
		ContentType: resp.Header.Get("Content-Type"),
		Body:        data,
	}, nil
}

func parseEnvelope(status int, data []byte) *Envelope {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return &Envelope{Success: status < 400}
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &probe); err == nil {
		if _, ok := probe["success"]; ok {
			var env Envelope
			if err := json.Unmarshal(trimmed, &env); err == nil {
				return &env
			}
		}
	}
	env := &Envelope{Success: status < 400}
	if json.Valid(trimmed) {
		env.Result = trimmed
	} else {
		quoted, _ := json.Marshal(string(data))
		env.Result = quoted
	}
	return env
}

// DoAutoPaginate follows page- or cursor-based pagination and returns a
// single envelope whose Result is the concatenation of every page's items.
// If the caller already pinned a page or cursor, or the result is not an
// array, it behaves like Do.
func (c *Client) DoAutoPaginate(ctx context.Context, r Request) (*Envelope, error) {
	if r.Query == nil {
		r.Query = url.Values{}
	}
	if r.Query.Get("page") != "" || r.Query.Get("cursor") != "" {
		return c.Do(ctx, r)
	}
	var merged []json.RawMessage
	var last *Envelope
	page := 0
	for i := 0; i < maxPages; i++ {
		env, err := c.Do(ctx, r)
		if err != nil {
			return env, err
		}
		last = env
		var items []json.RawMessage
		if err := json.Unmarshal(env.Result, &items); err != nil {
			if i == 0 {
				return env, nil // not a list endpoint
			}
			return env, fmt.Errorf("pagination: page %d result was not an array", i+1)
		}
		merged = append(merged, items...)
		ri := env.ResultInfo
		if ri == nil || len(items) == 0 {
			break
		}
		if after := ri.Cursors.After; after != "" {
			r.Query.Set("cursor", after)
			continue
		}
		if ri.Cursor != "" {
			r.Query.Set("cursor", ri.Cursor)
			continue
		}
		if ri.TotalPages > 0 {
			if page == 0 {
				page = ri.Page
				if page == 0 {
					page = 1
				}
			}
			if page >= ri.TotalPages {
				break
			}
			page++
			r.Query.Set("page", strconv.Itoa(page))
			continue
		}
		break
	}
	out, err := json.Marshal(merged)
	if err != nil {
		return nil, err
	}
	res := &Envelope{Success: true, Result: out}
	if last != nil {
		res.Messages = last.Messages
	}
	return res, nil
}
