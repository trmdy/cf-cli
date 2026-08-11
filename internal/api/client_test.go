package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestDoParsesEnvelope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("Authorization = %q", got)
		}
		fmt.Fprint(w, `{"success":true,"errors":[],"messages":[],"result":{"id":"abc"}}`)
	}))
	defer srv.Close()

	c := New(srv.URL, "tok", "test")
	env, err := c.Do(context.Background(), Request{Method: "GET", Path: "/zones"})
	if err != nil {
		t.Fatal(err)
	}
	if !env.Success || string(env.Result) != `{"id":"abc"}` {
		t.Errorf("unexpected envelope: %+v", env)
	}
}

func TestDoAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		fmt.Fprint(w, `{"success":false,"errors":[{"code":9109,"message":"Invalid access token"}],"result":null}`)
	}))
	defer srv.Close()

	c := New(srv.URL, "tok", "test")
	_, err := c.Do(context.Background(), Request{Method: "GET", Path: "/zones"})
	if err == nil {
		t.Fatal("expected error")
	}
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("expected *APIError, got %T", err)
	}
	if apiErr.StatusCode != 403 || len(apiErr.Errors) != 1 || apiErr.Errors[0].Code != 9109 {
		t.Errorf("unexpected APIError: %+v", apiErr)
	}
	if !strings.Contains(apiErr.Error(), "Invalid access token") {
		t.Errorf("error string missing message: %s", apiErr.Error())
	}
}

func TestDoNonEnvelopeResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `example.com. 300 IN A 192.0.2.1`)
	}))
	defer srv.Close()

	c := New(srv.URL, "tok", "test")
	env, err := c.Do(context.Background(), Request{Method: "GET", Path: "/export"})
	if err != nil {
		t.Fatal(err)
	}
	var s string
	if err := json.Unmarshal(env.Result, &s); err != nil || !strings.Contains(s, "example.com") {
		t.Errorf("raw body not preserved: %s", env.Result)
	}
}

func TestDoAutoPaginatePages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		switch page {
		case "", "1":
			fmt.Fprint(w, `{"success":true,"result":[{"n":1}],"result_info":{"page":1,"total_pages":3}}`)
		case "2":
			fmt.Fprint(w, `{"success":true,"result":[{"n":2}],"result_info":{"page":2,"total_pages":3}}`)
		case "3":
			fmt.Fprint(w, `{"success":true,"result":[{"n":3}],"result_info":{"page":3,"total_pages":3}}`)
		default:
			t.Errorf("unexpected page %q", page)
		}
	}))
	defer srv.Close()

	c := New(srv.URL, "tok", "test")
	env, err := c.DoAutoPaginate(context.Background(), Request{Method: "GET", Path: "/things"})
	if err != nil {
		t.Fatal(err)
	}
	var items []map[string]int
	if err := json.Unmarshal(env.Result, &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 || items[2]["n"] != 3 {
		t.Errorf("expected 3 merged items, got %v", items)
	}
}

func TestDoAutoPaginateCursor(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("cursor") {
		case "":
			fmt.Fprint(w, `{"success":true,"result":[{"n":1}],"result_info":{"cursors":{"after":"c2"}}}`)
		case "c2":
			fmt.Fprint(w, `{"success":true,"result":[{"n":2}],"result_info":{}}`)
		default:
			t.Errorf("unexpected cursor")
		}
	}))
	defer srv.Close()

	c := New(srv.URL, "tok", "test")
	env, err := c.DoAutoPaginate(context.Background(), Request{Method: "GET", Path: "/things"})
	if err != nil {
		t.Fatal(err)
	}
	var items []map[string]int
	if err := json.Unmarshal(env.Result, &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Errorf("expected 2 merged items, got %v", items)
	}
}

func TestDumpRedactsToken(t *testing.T) {
	c := New("", "supersecret", "test")
	q := url.Values{}
	q.Set("per_page", "50")
	d, err := c.Dump(Request{Method: "POST", Path: "/zones", Query: q, Body: []byte(`{"name":"x"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(d.Headers["Authorization"], "supersecret") {
		t.Error("token leaked into dump")
	}
	if d.URL != DefaultBaseURL+"/zones?per_page=50" {
		t.Errorf("unexpected URL: %s", d.URL)
	}
	if string(d.Body) != `{"name":"x"}` {
		t.Errorf("unexpected body: %s", d.Body)
	}
}

func TestDoRawReturnsBytesVerbatim(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ct := r.Header.Get("Content-Type"); r.Method == "PUT" && ct != "text/plain" {
			t.Errorf("Content-Type = %q, want text/plain", ct)
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write([]byte{0x00, 0x01, 0xFF, 'h', 'i'})
	}))
	defer srv.Close()

	c := New(srv.URL, "tok", "test")
	resp, err := c.DoRaw(context.Background(), Request{
		Method: "PUT", Path: "/kv", Body: []byte("raw value"), ContentType: "text/plain",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ContentType != "application/octet-stream" || len(resp.Body) != 5 || resp.Body[2] != 0xFF {
		t.Errorf("raw response mangled: %+v", resp)
	}
}

func TestDoRawErrorParsesEnvelope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		fmt.Fprint(w, `{"success":false,"errors":[{"code":10009,"message":"key not found"}]}`)
	}))
	defer srv.Close()

	c := New(srv.URL, "tok", "test")
	_, err := c.DoRaw(context.Background(), Request{Method: "GET", Path: "/kv"})
	apiErr, ok := err.(*APIError)
	if !ok || apiErr.StatusCode != 404 || !strings.Contains(apiErr.Error(), "key not found") {
		t.Fatalf("expected enveloped APIError, got %v", err)
	}
}

func TestDumpNonJSONContentType(t *testing.T) {
	c := New("", "tok", "test")
	d, err := c.Dump(Request{Method: "PUT", Path: "/kv", Body: []byte("plain text"), ContentType: "text/plain"})
	if err != nil {
		t.Fatal(err)
	}
	if d.Headers["Content-Type"] != "text/plain" {
		t.Errorf("dump content-type: %v", d.Headers)
	}
	if string(d.Body) != `"plain text"` {
		t.Errorf("dump body: %s", d.Body)
	}
}
