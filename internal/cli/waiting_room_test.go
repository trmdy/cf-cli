package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	waitingRoomTestZoneID  = "023e105f4ecef8ad9ca31a8372d0c353"
	waitingRoomTestAcctID  = "a1b2c3d4e5f60718293a4b5c6d7e8f90"
	waitingRoomTestRoomID  = "699d98642c564d2e855e9661899b7252"
	waitingRoomTestEventID = "25756b2dfe6e378a06b033b670413757"
	waitingRoomTestRuleID  = "25756b2dfe6e378a06b033b670413757"
)

func runWaitingRoomCLI(t *testing.T, serverURL string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	return runWaitingRoomCLIOpts(t, serverURL, true, args...)
}

// runWaitingRoomCLIOpts runs the real command tree. When withZone is false the
// global --zone-id is omitted so resolveZoneInteractive coverage can assert
// the "no zone specified" path without a configured default.
func runWaitingRoomCLIOpts(t *testing.T, serverURL string, withZone bool, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := NewRootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	all := []string{
		"--base-url", serverURL,
		"--token", "test-token",
		"--account-id", waitingRoomTestAcctID,
	}
	if withZone {
		all = append(all, "--zone-id", waitingRoomTestZoneID)
	}
	all = append(all, args...)
	root.SetArgs(all)
	err = root.Execute()
	return out.String(), errBuf.String(), err
}

// waitingRoomIsolateConfig blocks profile/env zone defaults from leaking into
// resolver tests.
func waitingRoomIsolateConfig(t *testing.T) {
	t.Helper()
	t.Setenv("CF_CONFIG_DIR", t.TempDir())
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "")
	t.Setenv("CF_ACCOUNT_ID", "")
	t.Setenv("CLOUDFLARE_ZONE_ID", "")
	t.Setenv("CF_ZONE_ID", "")
}

func waitingRoomAssertJSONEqual(t *testing.T, got []byte, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("got JSON: %v\n%s", err, got)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("want JSON: %v\n%s", err, want)
	}
	gb, _ := json.Marshal(g)
	wb, _ := json.Marshal(w)
	if string(gb) != string(wb) {
		t.Fatalf("JSON mismatch\ngot:  %s\nwant: %s", gb, wb)
	}
}

func TestWaitingRoomCreateDryRunCanonicalBody(t *testing.T) {
	stdout, _, err := runWaitingRoomCLI(t, "http://example.invalid",
		"waiting-room", "create",
		"--name", "production_webinar",
		"--host", "shop.example.com",
		"--new-users-per-minute", "200",
		"--total-active-users", "200",
		"--path", "/shop/checkout",
		"--queueing-method", "fifo",
		"--queueing-status-code", "202",
		"--session-duration", "1",
		"--turnstile-mode", "off",
		"--turnstile-action", "log",
		"--default-template-language", "es-ES",
		"--cookie-suffix", "abcd",
		"--additional-routes", `[{"host":"shop2.example.com","path":"/shop2/checkout"}]`,
		"--cookie-attributes", `{"samesite":"auto","secure":"auto"}`,
		"--enabled-origin-command", "revoke",
		"--queue-all=true",
		"--dry-run",
	)
	if err != nil {
		t.Fatal(err)
	}
	var dump struct {
		Method string          `json:"method"`
		URL    string          `json:"url"`
		Body   json.RawMessage `json:"body"`
	}
	if err := json.Unmarshal([]byte(stdout), &dump); err != nil {
		t.Fatal(err)
	}
	if dump.Method != "POST" || !strings.HasSuffix(dump.URL, "/zones/"+waitingRoomTestZoneID+"/waiting_rooms") {
		t.Fatalf("request = %s %s", dump.Method, dump.URL)
	}
	waitingRoomAssertJSONEqual(t, dump.Body, `{
		"name":"production_webinar",
		"host":"shop.example.com",
		"new_users_per_minute":200,
		"total_active_users":200,
		"path":"/shop/checkout",
		"queueing_method":"fifo",
		"queueing_status_code":202,
		"session_duration":1,
		"turnstile_mode":"off",
		"turnstile_action":"log",
		"default_template_language":"es-ES",
		"cookie_suffix":"abcd",
		"additional_routes":[{"host":"shop2.example.com","path":"/shop2/checkout"}],
		"cookie_attributes":{"samesite":"auto","secure":"auto"},
		"enabled_origin_commands":["revoke"],
		"queue_all":true
	}`)
}

func TestWaitingRoomCreateValidationBounds(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "new users below minimum",
			args: []string{"waiting-room", "create", "--name", "r", "--host", "shop.example.com",
				"--new-users-per-minute", "199", "--total-active-users", "200", "--dry-run"},
			want: "between 200 and",
		},
		{
			name: "new users at minimum",
			args: []string{"waiting-room", "create", "--name", "r", "--host", "shop.example.com",
				"--new-users-per-minute", "200", "--total-active-users", "200", "--dry-run"},
			want: "",
		},
		{
			name: "total users below minimum",
			args: []string{"waiting-room", "create", "--name", "r", "--host", "shop.example.com",
				"--new-users-per-minute", "200", "--total-active-users", "199", "--dry-run"},
			want: "between 200 and",
		},
		{
			name: "new users greater than total",
			args: []string{"waiting-room", "create", "--name", "r", "--host", "shop.example.com",
				"--new-users-per-minute", "300", "--total-active-users", "200", "--dry-run"},
			want: "less than or equal",
		},
		{
			name: "session duration below minimum",
			args: []string{"waiting-room", "create", "--name", "r", "--host", "shop.example.com",
				"--new-users-per-minute", "200", "--total-active-users", "200",
				"--session-duration", "0", "--dry-run"},
			want: "between 1 and 30",
		},
		{
			name: "session duration at minimum",
			args: []string{"waiting-room", "create", "--name", "r", "--host", "shop.example.com",
				"--new-users-per-minute", "200", "--total-active-users", "200",
				"--session-duration", "1", "--dry-run"},
			want: "",
		},
		{
			name: "session duration at maximum",
			args: []string{"waiting-room", "create", "--name", "r", "--host", "shop.example.com",
				"--new-users-per-minute", "200", "--total-active-users", "200",
				"--session-duration", "30", "--dry-run"},
			want: "",
		},
		{
			name: "session duration above maximum",
			args: []string{"waiting-room", "create", "--name", "r", "--host", "shop.example.com",
				"--new-users-per-minute", "200", "--total-active-users", "200",
				"--session-duration", "31", "--dry-run"},
			want: "between 1 and 30",
		},
		{
			name: "invalid queueing status code",
			args: []string{"waiting-room", "create", "--name", "r", "--host", "shop.example.com",
				"--new-users-per-minute", "200", "--total-active-users", "200",
				"--queueing-status-code", "201", "--dry-run"},
			want: "200, 202, 429",
		},
		{
			name: "canonical queueing status 429",
			args: []string{"waiting-room", "create", "--name", "r", "--host", "shop.example.com",
				"--new-users-per-minute", "200", "--total-active-users", "200",
				"--queueing-status-code", "429", "--dry-run"},
			want: "",
		},
		{
			name: "invalid name characters",
			args: []string{"waiting-room", "create", "--name", "bad name!", "--host", "shop.example.com",
				"--new-users-per-minute", "200", "--total-active-users", "200", "--dry-run"},
			want: "alphanumeric",
		},
		{
			name: "host with scheme",
			args: []string{"waiting-room", "create", "--name", "r", "--host", "https://shop.example.com",
				"--new-users-per-minute", "200", "--total-active-users", "200", "--dry-run"},
			want: "bare hostname",
		},
		{
			name: "host with wildcard",
			args: []string{"waiting-room", "create", "--name", "r", "--host", "*.example.com",
				"--new-users-per-minute", "200", "--total-active-users", "200", "--dry-run"},
			want: "bare hostname",
		},
		{
			name: "path with wildcard",
			args: []string{"waiting-room", "create", "--name", "r", "--host", "shop.example.com",
				"--new-users-per-minute", "200", "--total-active-users", "200",
				"--path", "/shop/*", "--dry-run"},
			want: "wildcards or query",
		},
		{
			name: "path with query",
			args: []string{"waiting-room", "create", "--name", "r", "--host", "shop.example.com",
				"--new-users-per-minute", "200", "--total-active-users", "200",
				"--path", "/shop?x=1", "--dry-run"},
			want: "wildcards or query",
		},
		{
			name: "path without wildcard or query ok",
			args: []string{"waiting-room", "create", "--name", "r", "--host", "shop.example.com",
				"--new-users-per-minute", "200", "--total-active-users", "200",
				"--path", "/shop/checkout", "--dry-run"},
			want: "",
		},
		{
			name: "invalid queueing method",
			args: []string{"waiting-room", "create", "--name", "r", "--host", "shop.example.com",
				"--new-users-per-minute", "200", "--total-active-users", "200",
				"--queueing-method", "LIFO", "--dry-run"},
			want: "queueing-method",
		},
		{
			name: "additional routes without cookie suffix",
			args: []string{"waiting-room", "create", "--name", "r", "--host", "shop.example.com",
				"--new-users-per-minute", "200", "--total-active-users", "200",
				"--additional-routes", `[{"host":"shop2.example.com"}]`, "--dry-run"},
			want: "cookie_suffix",
		},
		{
			name: "additional route host with scheme",
			args: []string{"waiting-room", "create", "--name", "r", "--host", "shop.example.com",
				"--new-users-per-minute", "200", "--total-active-users", "200",
				"--cookie-suffix", "x",
				"--additional-routes", `[{"host":"https://shop2.example.com"}]`, "--dry-run"},
			want: "bare hostname",
		},
		{
			name: "additional route host with wildcard",
			args: []string{"waiting-room", "create", "--name", "r", "--host", "shop.example.com",
				"--new-users-per-minute", "200", "--total-active-users", "200",
				"--cookie-suffix", "x",
				"--additional-routes", `[{"host":"*.example.com"}]`, "--dry-run"},
			want: "bare hostname",
		},
		{
			name: "additional route path with query",
			args: []string{"waiting-room", "create", "--name", "r", "--host", "shop.example.com",
				"--new-users-per-minute", "200", "--total-active-users", "200",
				"--cookie-suffix", "x",
				"--additional-routes", `[{"host":"shop2.example.com","path":"/a?b=1"}]`, "--dry-run"},
			want: "wildcards or query",
		},
		{
			name: "additional route path with wildcard",
			args: []string{"waiting-room", "create", "--name", "r", "--host", "shop.example.com",
				"--new-users-per-minute", "200", "--total-active-users", "200",
				"--cookie-suffix", "x",
				"--additional-routes", `[{"host":"shop2.example.com","path":"/a/*"}]`, "--dry-run"},
			want: "wildcards or query",
		},
		{
			name: "additional route clean host/path ok",
			args: []string{"waiting-room", "create", "--name", "r", "--host", "shop.example.com",
				"--new-users-per-minute", "200", "--total-active-users", "200",
				"--cookie-suffix", "x",
				"--additional-routes", `[{"host":"shop2.example.com","path":"/shop2"}]`, "--dry-run"},
			want: "",
		},
		{
			name: "additional routes null rejected",
			args: []string{"waiting-room", "create", "--name", "r", "--host", "shop.example.com",
				"--new-users-per-minute", "200", "--total-active-users", "200",
				"--cookie-suffix", "x", "--additional-routes", "null", "--dry-run"},
			want: "JSON array",
		},
		{
			name: "cookie attributes null rejected",
			args: []string{"waiting-room", "create", "--name", "r", "--host", "shop.example.com",
				"--new-users-per-minute", "200", "--total-active-users", "200",
				"--cookie-attributes", "null", "--dry-run"},
			want: "JSON object",
		},
		{
			name: "cookie attributes array rejected",
			args: []string{"waiting-room", "create", "--name", "r", "--host", "shop.example.com",
				"--new-users-per-minute", "200", "--total-active-users", "200",
				"--cookie-attributes", `[]`, "--dry-run"},
			want: "JSON object",
		},
		{
			name: "cookie samesite none with secure never",
			args: []string{"waiting-room", "create", "--name", "r", "--host", "shop.example.com",
				"--new-users-per-minute", "200", "--total-active-users", "200",
				"--cookie-attributes", `{"samesite":"none","secure":"never"}`, "--dry-run"},
			want: "samesite=none",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := runWaitingRoomCLI(t, "http://example.invalid", tc.args...)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestWaitingRoomUpdateValidationIndependent(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "nothing to update",
			args: []string{"waiting-room", "update", waitingRoomTestRoomID, "--dry-run"},
			want: "nothing to update",
		},
		{
			name: "session duration below on update",
			args: []string{"waiting-room", "update", waitingRoomTestRoomID,
				"--session-duration", "0", "--dry-run"},
			want: "between 1 and 30",
		},
		{
			name: "session duration above on update",
			args: []string{"waiting-room", "update", waitingRoomTestRoomID,
				"--session-duration", "31", "--dry-run"},
			want: "between 1 and 30",
		},
		{
			name: "users pair on update flags",
			args: []string{"waiting-room", "update", waitingRoomTestRoomID,
				"--new-users-per-minute", "500", "--total-active-users", "400", "--dry-run"},
			want: "less than or equal",
		},
		{
			name: "additional routes null on update",
			args: []string{"waiting-room", "update", waitingRoomTestRoomID,
				"--additional-routes", "null", "--dry-run"},
			want: "JSON array",
		},
		{
			name: "path wildcard on update",
			args: []string{"waiting-room", "update", waitingRoomTestRoomID,
				"--path", "/x*", "--dry-run"},
			want: "wildcards or query",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := runWaitingRoomCLI(t, "http://example.invalid", tc.args...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func waitingRoomCurrentObject() map[string]any {
	return map[string]any{
		"id":                    waitingRoomTestRoomID,
		"created_on":            "2014-01-01T05:20:00.12345Z",
		"modified_on":           "2014-01-01T05:20:00.12345Z",
		"name":                  "production_webinar",
		"host":                  "shop.example.com",
		"path":                  "/shop/checkout",
		"new_users_per_minute":  200,
		"total_active_users":    300,
		"session_duration":      5,
		"queueing_method":       "fifo",
		"cookie_suffix":         "abcd",
		"additional_routes":     []any{map[string]any{"host": "shop2.example.com", "path": "/shop2"}},
		"future_feature_flag":   true,
		"suspended":             false,
		"next_event_start_time": "2021-09-28T15:00:00.000Z",
	}
}

func TestWaitingRoomUpdateGETMergeDryRunBody(t *testing.T) {
	var gotMethods []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethods = append(gotMethods, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "GET" {
			raw, _ := json.Marshal(map[string]any{"success": true, "result": waitingRoomCurrentObject()})
			_, _ = w.Write(raw)
			return
		}
		t.Fatalf("unexpected write during dry-run: %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	stdout, _, err := runWaitingRoomCLI(t, srv.URL,
		"waiting-room", "update", waitingRoomTestRoomID,
		"--session-duration", "10",
		"--suspended=true",
		"--dry-run",
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(gotMethods) != 1 || !strings.HasPrefix(gotMethods[0], "GET ") {
		t.Fatalf("methods = %v, want single GET (dry-run still reads)", gotMethods)
	}
	var dump struct {
		Method string          `json:"method"`
		URL    string          `json:"url"`
		Body   json.RawMessage `json:"body"`
	}
	if err := json.Unmarshal([]byte(stdout), &dump); err != nil {
		t.Fatal(err)
	}
	wantPath := "/zones/" + waitingRoomTestZoneID + "/waiting_rooms/" + waitingRoomTestRoomID
	if dump.Method != "PATCH" || !strings.HasSuffix(dump.URL, wantPath) {
		t.Fatalf("request = %s %s", dump.Method, dump.URL)
	}
	// Required fields from GET, patch applied, unknown field preserved, read-only stripped.
	waitingRoomAssertJSONEqual(t, dump.Body, `{
		"name":"production_webinar",
		"host":"shop.example.com",
		"path":"/shop/checkout",
		"new_users_per_minute":200,
		"total_active_users":300,
		"session_duration":10,
		"queueing_method":"fifo",
		"cookie_suffix":"abcd",
		"additional_routes":[{"host":"shop2.example.com","path":"/shop2"}],
		"future_feature_flag":true,
		"suspended":true
	}`)
}

func TestWaitingRoomUpdateMergeValidatesUserPair(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		cur := waitingRoomCurrentObject()
		cur["total_active_users"] = 300
		raw, _ := json.Marshal(map[string]any{"success": true, "result": cur})
		_, _ = w.Write(raw)
	}))
	defer srv.Close()

	_, _, err := runWaitingRoomCLI(t, srv.URL,
		"waiting-room", "update", waitingRoomTestRoomID,
		"--new-users-per-minute", "500",
		"--dry-run",
	)
	if err == nil || !strings.Contains(err.Error(), "less than or equal") {
		t.Fatalf("error = %v, want post-merge user pair failure", err)
	}
}

func TestWaitingRoomUpdateMergeRequiresCookieSuffix(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		cur := waitingRoomCurrentObject()
		delete(cur, "cookie_suffix")
		delete(cur, "additional_routes")
		raw, _ := json.Marshal(map[string]any{"success": true, "result": cur})
		_, _ = w.Write(raw)
	}))
	defer srv.Close()

	// Setting routes without a suffix on the current object must fail after merge.
	_, _, err := runWaitingRoomCLI(t, srv.URL,
		"waiting-room", "update", waitingRoomTestRoomID,
		"--additional-routes", `[{"host":"shop3.example.com","path":"/x"}]`,
		"--dry-run",
	)
	if err == nil || !strings.Contains(err.Error(), "cookie_suffix") {
		t.Fatalf("error = %v, want cookie_suffix after merge", err)
	}

	// Existing suffix on the room satisfies the routes constraint without a same-request flag.
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		cur := waitingRoomCurrentObject()
		cur["cookie_suffix"] = "kept"
		delete(cur, "additional_routes")
		raw, _ := json.Marshal(map[string]any{"success": true, "result": cur})
		_, _ = w.Write(raw)
	}))
	defer srv2.Close()
	if _, _, err := runWaitingRoomCLI(t, srv2.URL,
		"waiting-room", "update", waitingRoomTestRoomID,
		"--additional-routes", `[{"host":"shop3.example.com","path":"/x"}]`,
		"--dry-run",
	); err != nil {
		t.Fatalf("unexpected error when suffix already present: %v", err)
	}
}

func TestWaitingRoomEventsCreateValidation(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "start not one minute before end",
			args: []string{"waiting-room", "events", "create", waitingRoomTestRoomID,
				"--name", "e1",
				"--event-start-time", "2021-09-28T15:30:00.000Z",
				"--event-end-time", "2021-09-28T15:30:30.000Z",
				"--dry-run"},
			want: "one minute",
		},
		{
			name: "start exactly one minute before end",
			args: []string{"waiting-room", "events", "create", waitingRoomTestRoomID,
				"--name", "e1",
				"--event-start-time", "2021-09-28T15:30:00.000Z",
				"--event-end-time", "2021-09-28T15:31:00.000Z",
				"--dry-run"},
			want: "",
		},
		{
			name: "prequeue less than five minutes",
			args: []string{"waiting-room", "events", "create", waitingRoomTestRoomID,
				"--name", "e1",
				"--event-start-time", "2021-09-28T15:30:00.000Z",
				"--event-end-time", "2021-09-28T17:00:00.000Z",
				"--prequeue-start-time", "2021-09-28T15:26:00.000Z",
				"--dry-run"},
			want: "five minutes",
		},
		{
			name: "prequeue exactly five minutes",
			args: []string{"waiting-room", "events", "create", waitingRoomTestRoomID,
				"--name", "e1",
				"--event-start-time", "2021-09-28T15:30:00.000Z",
				"--event-end-time", "2021-09-28T17:00:00.000Z",
				"--prequeue-start-time", "2021-09-28T15:25:00.000Z",
				"--dry-run"},
			want: "",
		},
		{
			name: "users must be paired",
			args: []string{"waiting-room", "events", "create", waitingRoomTestRoomID,
				"--name", "e1",
				"--event-start-time", "2021-09-28T15:30:00.000Z",
				"--event-end-time", "2021-09-28T17:00:00.000Z",
				"--new-users-per-minute", "200",
				"--dry-run"},
			want: "set together",
		},
		{
			name: "invalid event name",
			args: []string{"waiting-room", "events", "create", waitingRoomTestRoomID,
				"--name", "bad name",
				"--event-start-time", "2021-09-28T15:30:00.000Z",
				"--event-end-time", "2021-09-28T17:00:00.000Z",
				"--dry-run"},
			want: "alphanumeric",
		},
		{
			name: "shuffle requires prequeue",
			args: []string{"waiting-room", "events", "create", waitingRoomTestRoomID,
				"--name", "e1",
				"--event-start-time", "2021-09-28T15:30:00.000Z",
				"--event-end-time", "2021-09-28T17:00:00.000Z",
				"--shuffle-at-event-start",
				"--dry-run"},
			want: "prequeue_start_time",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := runWaitingRoomCLI(t, "http://example.invalid", tc.args...)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestWaitingRoomEventsCreateDryRunBody(t *testing.T) {
	stdout, _, err := runWaitingRoomCLI(t, "http://example.invalid",
		"waiting-room", "events", "create", waitingRoomTestRoomID,
		"--name", "production_webinar_event",
		"--event-start-time", "2021-09-28T15:30:00.000Z",
		"--event-end-time", "2021-09-28T17:00:00.000Z",
		"--prequeue-start-time", "2021-09-28T15:00:00.000Z",
		"--queueing-method", "random",
		"--new-users-per-minute", "200",
		"--total-active-users", "200",
		"--dry-run",
	)
	if err != nil {
		t.Fatal(err)
	}
	var dump struct {
		Method string          `json:"method"`
		URL    string          `json:"url"`
		Body   json.RawMessage `json:"body"`
	}
	if err := json.Unmarshal([]byte(stdout), &dump); err != nil {
		t.Fatal(err)
	}
	wantPath := "/zones/" + waitingRoomTestZoneID + "/waiting_rooms/" + waitingRoomTestRoomID + "/events"
	if dump.Method != "POST" || !strings.HasSuffix(dump.URL, wantPath) {
		t.Fatalf("request = %s %s", dump.Method, dump.URL)
	}
	waitingRoomAssertJSONEqual(t, dump.Body, `{
		"name":"production_webinar_event",
		"event_start_time":"2021-09-28T15:30:00.000Z",
		"event_end_time":"2021-09-28T17:00:00.000Z",
		"prequeue_start_time":"2021-09-28T15:00:00.000Z",
		"queueing_method":"random",
		"new_users_per_minute":200,
		"total_active_users":200
	}`)
}

func TestWaitingRoomEventsUpdateValidationIndependent(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "nothing to update",
			args: []string{"waiting-room", "events", "update", waitingRoomTestRoomID, waitingRoomTestEventID, "--dry-run"},
			want: "nothing to update",
		},
		{
			name: "paired users on update",
			args: []string{"waiting-room", "events", "update", waitingRoomTestRoomID, waitingRoomTestEventID,
				"--total-active-users", "300", "--dry-run"},
			want: "set together",
		},
		{
			name: "session bounds on update low",
			args: []string{"waiting-room", "events", "update", waitingRoomTestRoomID, waitingRoomTestEventID,
				"--session-duration", "0", "--dry-run"},
			want: "between 1 and 30",
		},
		{
			name: "session bounds on update high",
			args: []string{"waiting-room", "events", "update", waitingRoomTestRoomID, waitingRoomTestEventID,
				"--session-duration", "31", "--dry-run"},
			want: "between 1 and 30",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := runWaitingRoomCLI(t, "http://example.invalid", tc.args...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func waitingRoomCurrentEvent() map[string]any {
	return map[string]any{
		"id":                     waitingRoomTestEventID,
		"created_on":             "2014-01-01T05:20:00.12345Z",
		"modified_on":            "2014-01-01T05:20:00.12345Z",
		"name":                   "production_webinar_event",
		"event_start_time":       "2021-09-28T15:30:00.000Z",
		"event_end_time":         "2021-09-28T17:00:00.000Z",
		"prequeue_start_time":    "2021-09-28T15:00:00.000Z",
		"queueing_method":        "random",
		"suspended":              false,
		"event_custom_analytics": map[string]any{"cohort": "A"},
	}
}

func TestWaitingRoomEventUpdateGETMergeDryRunBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != "GET" {
			t.Fatalf("unexpected %s during dry-run", r.Method)
		}
		raw, _ := json.Marshal(map[string]any{"success": true, "result": waitingRoomCurrentEvent()})
		_, _ = w.Write(raw)
	}))
	defer srv.Close()

	stdout, _, err := runWaitingRoomCLI(t, srv.URL,
		"waiting-room", "events", "update", waitingRoomTestRoomID, waitingRoomTestEventID,
		"--suspended=true",
		"--dry-run",
	)
	if err != nil {
		t.Fatal(err)
	}
	var dump struct {
		Method string          `json:"method"`
		URL    string          `json:"url"`
		Body   json.RawMessage `json:"body"`
	}
	if err := json.Unmarshal([]byte(stdout), &dump); err != nil {
		t.Fatal(err)
	}
	wantPath := "/zones/" + waitingRoomTestZoneID + "/waiting_rooms/" + waitingRoomTestRoomID + "/events/" + waitingRoomTestEventID
	if dump.Method != "PATCH" || !strings.HasSuffix(dump.URL, wantPath) {
		t.Fatalf("request = %s %s", dump.Method, dump.URL)
	}
	waitingRoomAssertJSONEqual(t, dump.Body, `{
		"name":"production_webinar_event",
		"event_start_time":"2021-09-28T15:30:00.000Z",
		"event_end_time":"2021-09-28T17:00:00.000Z",
		"prequeue_start_time":"2021-09-28T15:00:00.000Z",
		"queueing_method":"random",
		"suspended":true,
		"event_custom_analytics":{"cohort":"A"}
	}`)
}

func TestWaitingRoomRulesValidation(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "create invalid action",
			args: []string{"waiting-room", "rules", "create", waitingRoomTestRoomID,
				"--action", "allow", "--expression", "true", "--dry-run"},
			want: "bypass_waiting_room",
		},
		{
			name: "replace null rules",
			args: []string{"waiting-room", "rules", "replace", waitingRoomTestRoomID,
				"--rules", "null", "--dry-run"},
			want: "JSON array",
		},
		{
			name: "replace object rules",
			args: []string{"waiting-room", "rules", "replace", waitingRoomTestRoomID,
				"--rules", `{"action":"bypass_waiting_room","expression":"true"}`, "--dry-run"},
			want: "JSON array",
		},
		{
			name: "replace missing expression",
			args: []string{"waiting-room", "rules", "replace", waitingRoomTestRoomID,
				"--rules", `[{"action":"bypass_waiting_room"}]`, "--dry-run"},
			want: "expression",
		},
		{
			name: "update nothing",
			args: []string{"waiting-room", "rules", "update", waitingRoomTestRoomID, waitingRoomTestRuleID, "--dry-run"},
			want: "nothing to update",
		},
		{
			name: "position null",
			args: []string{"waiting-room", "rules", "update", waitingRoomTestRoomID, waitingRoomTestRuleID,
				"--position", "null", "--dry-run"},
			want: "JSON object",
		},
		{
			name: "position both keys",
			args: []string{"waiting-room", "rules", "update", waitingRoomTestRoomID, waitingRoomTestRuleID,
				"--position", `{"index":1,"before":"x"}`, "--dry-run"},
			want: "exactly one",
		},
		{
			name: "position index zero",
			args: []string{"waiting-room", "rules", "update", waitingRoomTestRoomID, waitingRoomTestRuleID,
				"--position", `{"index":0}`, "--dry-run"},
			want: "integer starting at 1",
		},
		{
			name: "position index negative",
			args: []string{"waiting-room", "rules", "update", waitingRoomTestRoomID, waitingRoomTestRuleID,
				"--position", `{"index":-1}`, "--dry-run"},
			want: "integer starting at 1",
		},
		{
			name: "position index fractional",
			args: []string{"waiting-room", "rules", "update", waitingRoomTestRoomID, waitingRoomTestRuleID,
				"--position", `{"index":1.5}`, "--dry-run"},
			want: "integer starting at 1",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := runWaitingRoomCLI(t, "http://example.invalid", tc.args...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestWaitingRoomPositionIndexBoundaryOK(t *testing.T) {
	// index=1 is the lower bound; requires GET list of rules for merge.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != "GET" {
			t.Fatalf("unexpected %s", r.Method)
		}
		_, _ = w.Write([]byte(`{"success":true,"result":[{
			"id":"` + waitingRoomTestRuleID + `",
			"action":"bypass_waiting_room",
			"expression":"ip.src in {10.20.30.40}",
			"enabled":true,
			"description":"office",
			"version":"1",
			"last_updated":"2014-01-01T05:20:00.12345Z",
			"rule_meta":{"source":"ui"}
		}]}`))
	}))
	defer srv.Close()

	stdout, _, err := runWaitingRoomCLI(t, srv.URL,
		"waiting-room", "rules", "update", waitingRoomTestRoomID, waitingRoomTestRuleID,
		"--position", `{"index":1}`,
		"--dry-run",
	)
	if err != nil {
		t.Fatal(err)
	}
	var dump struct {
		Body json.RawMessage `json:"body"`
	}
	if err := json.Unmarshal([]byte(stdout), &dump); err != nil {
		t.Fatal(err)
	}
	waitingRoomAssertJSONEqual(t, dump.Body, `{
		"action":"bypass_waiting_room",
		"expression":"ip.src in {10.20.30.40}",
		"enabled":true,
		"description":"office",
		"rule_meta":{"source":"ui"},
		"position":{"index":1}
	}`)
}

func TestWaitingRoomRuleUpdateGETMergeEnabledOnly(t *testing.T) {
	var methods []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "GET" {
			_, _ = w.Write([]byte(`{"success":true,"result":[{
				"id":"` + waitingRoomTestRuleID + `",
				"action":"bypass_waiting_room",
				"expression":"true",
				"enabled":true,
				"version":"3",
				"last_updated":"2014-01-01T05:20:00.12345Z"
			}]}`))
			return
		}
		t.Fatalf("unexpected write %s", r.Method)
	}))
	defer srv.Close()

	stdout, _, err := runWaitingRoomCLI(t, srv.URL,
		"waiting-room", "rules", "update", waitingRoomTestRoomID, waitingRoomTestRuleID,
		"--enabled=false",
		"--dry-run",
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(methods) != 1 || methods[0] != "GET" {
		t.Fatalf("methods = %v", methods)
	}
	var dump struct {
		Body json.RawMessage `json:"body"`
	}
	if err := json.Unmarshal([]byte(stdout), &dump); err != nil {
		t.Fatal(err)
	}
	waitingRoomAssertJSONEqual(t, dump.Body, `{
		"action":"bypass_waiting_room",
		"expression":"true",
		"enabled":false
	}`)
}

func TestWaitingRoomRulesCreateDryRun(t *testing.T) {
	stdout, _, err := runWaitingRoomCLI(t, "http://example.invalid",
		"waiting-room", "rules", "create", waitingRoomTestRoomID,
		"--expression", "ip.src in {10.20.30.40}",
		"--description", "allow office",
		"--enabled=true",
		"--dry-run",
	)
	if err != nil {
		t.Fatal(err)
	}
	var dump struct {
		Method string          `json:"method"`
		URL    string          `json:"url"`
		Body   json.RawMessage `json:"body"`
	}
	if err := json.Unmarshal([]byte(stdout), &dump); err != nil {
		t.Fatal(err)
	}
	wantPath := "/zones/" + waitingRoomTestZoneID + "/waiting_rooms/" + waitingRoomTestRoomID + "/rules"
	if dump.Method != "POST" || !strings.HasSuffix(dump.URL, wantPath) {
		t.Fatalf("request = %s %s", dump.Method, dump.URL)
	}
	waitingRoomAssertJSONEqual(t, dump.Body, `{
		"action":"bypass_waiting_room",
		"expression":"ip.src in {10.20.30.40}",
		"description":"allow office",
		"enabled":true
	}`)
}

func TestWaitingRoomRulesReplaceDryRun(t *testing.T) {
	stdout, _, err := runWaitingRoomCLI(t, "http://example.invalid",
		"waiting-room", "rules", "replace", waitingRoomTestRoomID,
		"--rules", `[{"action":"bypass_waiting_room","expression":"ip.src in {10.20.30.40}","enabled":true}]`,
		"--dry-run",
	)
	if err != nil {
		t.Fatal(err)
	}
	var dump struct {
		Method string          `json:"method"`
		URL    string          `json:"url"`
		Body   json.RawMessage `json:"body"`
	}
	if err := json.Unmarshal([]byte(stdout), &dump); err != nil {
		t.Fatal(err)
	}
	wantPath := "/zones/" + waitingRoomTestZoneID + "/waiting_rooms/" + waitingRoomTestRoomID + "/rules"
	if dump.Method != "PUT" || !strings.HasSuffix(dump.URL, wantPath) {
		t.Fatalf("request = %s %s", dump.Method, dump.URL)
	}
	waitingRoomAssertJSONEqual(t, dump.Body, `[{"action":"bypass_waiting_room","expression":"ip.src in {10.20.30.40}","enabled":true}]`)
}

func TestWaitingRoomPreviewDryRun(t *testing.T) {
	stdout, _, err := runWaitingRoomCLI(t, "http://example.invalid",
		"waiting-room", "preview",
		"--custom-html", "{{#waitTimeKnown}}{{waitTime}} mins{{/waitTimeKnown}}",
		"--dry-run",
	)
	if err != nil {
		t.Fatal(err)
	}
	var dump struct {
		Method string          `json:"method"`
		URL    string          `json:"url"`
		Body   json.RawMessage `json:"body"`
	}
	if err := json.Unmarshal([]byte(stdout), &dump); err != nil {
		t.Fatal(err)
	}
	if dump.Method != "POST" || !strings.HasSuffix(dump.URL, "/zones/"+waitingRoomTestZoneID+"/waiting_rooms/preview") {
		t.Fatalf("request = %s %s", dump.Method, dump.URL)
	}
	waitingRoomAssertJSONEqual(t, dump.Body, `{"custom_html":"{{#waitTimeKnown}}{{waitTime}} mins{{/waitTimeKnown}}"}`)
}

func TestWaitingRoomHTTPCommands(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		wantMethod string
		wantPath   string
		wantBody   string
		response   string
	}{
		{
			name:       "list zone",
			args:       []string{"waiting-room", "list", "--output", "json"},
			wantMethod: "GET",
			wantPath:   "/zones/" + waitingRoomTestZoneID + "/waiting_rooms",
			response:   `{"success":true,"result":[{"id":"` + waitingRoomTestRoomID + `","name":"checkout","host":"shop.example.com","path":"/","new_users_per_minute":200,"total_active_users":300,"queueing_method":"fifo"}],"result_info":{"page":1,"per_page":100,"total_pages":1,"count":1,"total_count":1}}`,
		},
		{
			name:       "list account",
			args:       []string{"waiting-room", "list", "--scope", "account", "--output", "json"},
			wantMethod: "GET",
			wantPath:   "/accounts/" + waitingRoomTestAcctID + "/waiting_rooms",
			response:   `{"success":true,"result":[],"result_info":{"page":1,"per_page":100,"total_pages":1,"count":0,"total_count":0}}`,
		},
		{
			name:       "get",
			args:       []string{"waiting-room", "get", waitingRoomTestRoomID, "--output", "json"},
			wantMethod: "GET",
			wantPath:   "/zones/" + waitingRoomTestZoneID + "/waiting_rooms/" + waitingRoomTestRoomID,
			response:   `{"success":true,"result":{"id":"` + waitingRoomTestRoomID + `","name":"checkout"}}`,
		},
		{
			name:       "status",
			args:       []string{"waiting-room", "status", waitingRoomTestRoomID, "--output", "json"},
			wantMethod: "GET",
			wantPath:   "/zones/" + waitingRoomTestZoneID + "/waiting_rooms/" + waitingRoomTestRoomID + "/status",
			response:   `{"success":true,"result":{"status":"queueing","estimated_queued_users":10}}`,
		},
		{
			name:       "delete",
			args:       []string{"waiting-room", "delete", waitingRoomTestRoomID, "--force", "--output", "json"},
			wantMethod: "DELETE",
			wantPath:   "/zones/" + waitingRoomTestZoneID + "/waiting_rooms/" + waitingRoomTestRoomID,
			response:   `{"success":true,"result":{"id":"` + waitingRoomTestRoomID + `"}}`,
		},
		{
			name:       "events list",
			args:       []string{"waiting-room", "events", "list", waitingRoomTestRoomID, "--output", "json"},
			wantMethod: "GET",
			wantPath:   "/zones/" + waitingRoomTestZoneID + "/waiting_rooms/" + waitingRoomTestRoomID + "/events",
			response:   `{"success":true,"result":[{"id":"` + waitingRoomTestEventID + `","name":"sale"}],"result_info":{"page":1,"per_page":100,"total_pages":1,"count":1,"total_count":1}}`,
		},
		{
			name:       "events get",
			args:       []string{"waiting-room", "events", "get", waitingRoomTestRoomID, waitingRoomTestEventID, "--output", "json"},
			wantMethod: "GET",
			wantPath:   "/zones/" + waitingRoomTestZoneID + "/waiting_rooms/" + waitingRoomTestRoomID + "/events/" + waitingRoomTestEventID,
			response:   `{"success":true,"result":{"id":"` + waitingRoomTestEventID + `"}}`,
		},
		{
			name:       "events details",
			args:       []string{"waiting-room", "events", "details", waitingRoomTestRoomID, waitingRoomTestEventID, "--output", "json"},
			wantMethod: "GET",
			wantPath:   "/zones/" + waitingRoomTestZoneID + "/waiting_rooms/" + waitingRoomTestRoomID + "/events/" + waitingRoomTestEventID + "/details",
			response:   `{"success":true,"result":{"id":"` + waitingRoomTestEventID + `","name":"sale"}}`,
		},
		{
			name:       "events delete",
			args:       []string{"waiting-room", "events", "delete", waitingRoomTestRoomID, waitingRoomTestEventID, "--force", "--output", "json"},
			wantMethod: "DELETE",
			wantPath:   "/zones/" + waitingRoomTestZoneID + "/waiting_rooms/" + waitingRoomTestRoomID + "/events/" + waitingRoomTestEventID,
			response:   `{"success":true,"result":{"id":"` + waitingRoomTestEventID + `"}}`,
		},
		{
			name:       "rules list",
			args:       []string{"waiting-room", "rules", "list", waitingRoomTestRoomID, "--output", "json"},
			wantMethod: "GET",
			wantPath:   "/zones/" + waitingRoomTestZoneID + "/waiting_rooms/" + waitingRoomTestRoomID + "/rules",
			response:   `{"success":true,"result":[{"id":"` + waitingRoomTestRuleID + `","action":"bypass_waiting_room","expression":"true","enabled":true}]}`,
		},
		{
			name:       "rules delete",
			args:       []string{"waiting-room", "rules", "delete", waitingRoomTestRoomID, waitingRoomTestRuleID, "--force", "--output", "json"},
			wantMethod: "DELETE",
			wantPath:   "/zones/" + waitingRoomTestZoneID + "/waiting_rooms/" + waitingRoomTestRoomID + "/rules/" + waitingRoomTestRuleID,
			response:   `{"success":true,"result":[{"id":"` + waitingRoomTestRuleID + `"}]}`,
		},
		{
			name:       "preview http",
			args:       []string{"waiting-room", "preview", "--custom-html", "<p>hi</p>", "--output", "json"},
			wantMethod: "POST",
			wantPath:   "/zones/" + waitingRoomTestZoneID + "/waiting_rooms/preview",
			wantBody:   `{"custom_html":"<p>hi</p>"}`,
			response:   `{"success":true,"result":{"preview_url":"http://waitingrooms.dev/preview/abc"}}`,
		},
		{
			name: "create http",
			args: []string{"waiting-room", "create",
				"--name", "checkout", "--host", "shop.example.com",
				"--new-users-per-minute", "200", "--total-active-users", "300",
				"--output", "json"},
			wantMethod: "POST",
			wantPath:   "/zones/" + waitingRoomTestZoneID + "/waiting_rooms",
			wantBody:   `{"name":"checkout","host":"shop.example.com","new_users_per_minute":200,"total_active_users":300}`,
			response:   `{"success":true,"result":{"id":"` + waitingRoomTestRoomID + `","name":"checkout"}}`,
		},
		{
			name: "events create http",
			args: []string{"waiting-room", "events", "create", waitingRoomTestRoomID,
				"--name", "sale_open",
				"--event-start-time", "2021-09-28T15:30:00.000Z",
				"--event-end-time", "2021-09-28T17:00:00.000Z",
				"--output", "json"},
			wantMethod: "POST",
			wantPath:   "/zones/" + waitingRoomTestZoneID + "/waiting_rooms/" + waitingRoomTestRoomID + "/events",
			wantBody:   `{"name":"sale_open","event_start_time":"2021-09-28T15:30:00.000Z","event_end_time":"2021-09-28T17:00:00.000Z"}`,
			response:   `{"success":true,"result":{"id":"` + waitingRoomTestEventID + `"}}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotMethod, gotPath string
			var gotBody []byte
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotMethod = r.Method
				gotPath = r.URL.Path
				if r.Body != nil {
					gotBody, _ = io.ReadAll(r.Body)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.response))
			}))
			defer srv.Close()

			stdout, _, err := runWaitingRoomCLI(t, srv.URL, tc.args...)
			if err != nil {
				t.Fatal(err)
			}
			if gotMethod != tc.wantMethod || gotPath != tc.wantPath {
				t.Fatalf("request = %s %s, want %s %s", gotMethod, gotPath, tc.wantMethod, tc.wantPath)
			}
			if tc.wantBody != "" {
				waitingRoomAssertJSONEqual(t, gotBody, tc.wantBody)
			}
			if !json.Valid([]byte(stdout)) && !strings.Contains(stdout, "ID") {
				// table or json both fine depending on default; force json in cases
				if strings.Contains(strings.Join(tc.args, " "), "--output json") && !json.Valid([]byte(stdout)) {
					t.Fatalf("expected JSON stdout, got %q", stdout)
				}
			}
		})
	}
}

func TestWaitingRoomRulesUpdateHTTPGetThenPatch(t *testing.T) {
	var methods []string
	var patchBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/rules"):
			_, _ = w.Write([]byte(`{"success":true,"result":[{
				"id":"` + waitingRoomTestRuleID + `",
				"action":"bypass_waiting_room",
				"expression":"ip.src in {10.20.30.40}",
				"enabled":true,
				"description":"office",
				"version":"2",
				"last_updated":"2014-01-01T05:20:00.12345Z"
			}]}`))
		case r.Method == "PATCH":
			patchBody, _ = io.ReadAll(r.Body)
			_, _ = w.Write([]byte(`{"success":true,"result":[{"id":"` + waitingRoomTestRuleID + `","enabled":false}]}`))
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	if _, _, err := runWaitingRoomCLI(t, srv.URL,
		"waiting-room", "rules", "update", waitingRoomTestRoomID, waitingRoomTestRuleID,
		"--enabled=false", "--position", `{"index":1}`, "--output", "json"); err != nil {
		t.Fatal(err)
	}
	if len(methods) != 2 || !strings.HasPrefix(methods[0], "GET ") || !strings.HasPrefix(methods[1], "PATCH ") {
		t.Fatalf("methods = %v", methods)
	}
	waitingRoomAssertJSONEqual(t, patchBody, `{
		"action":"bypass_waiting_room",
		"expression":"ip.src in {10.20.30.40}",
		"description":"office",
		"enabled":false,
		"position":{"index":1}
	}`)
}

func TestWaitingRoomUpdateHTTPGetThenPatch(t *testing.T) {
	var methods []string
	var patchBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case "GET":
			raw, _ := json.Marshal(map[string]any{"success": true, "result": waitingRoomCurrentObject()})
			_, _ = w.Write(raw)
		case "PATCH":
			patchBody, _ = io.ReadAll(r.Body)
			_, _ = w.Write([]byte(`{"success":true,"result":{"id":"` + waitingRoomTestRoomID + `"}}`))
		default:
			t.Fatalf("unexpected %s", r.Method)
		}
	}))
	defer srv.Close()

	if _, _, err := runWaitingRoomCLI(t, srv.URL,
		"waiting-room", "update", waitingRoomTestRoomID,
		"--total-active-users", "500", "--output", "json"); err != nil {
		t.Fatal(err)
	}
	if len(methods) != 2 || methods[0] != "GET" || methods[1] != "PATCH" {
		t.Fatalf("methods = %v", methods)
	}
	waitingRoomAssertJSONEqual(t, patchBody, `{
		"name":"production_webinar",
		"host":"shop.example.com",
		"path":"/shop/checkout",
		"new_users_per_minute":200,
		"total_active_users":500,
		"session_duration":5,
		"queueing_method":"fifo",
		"cookie_suffix":"abcd",
		"additional_routes":[{"host":"shop2.example.com","path":"/shop2"}],
		"future_feature_flag":true,
		"suspended":false
	}`)
}

func TestWaitingRoomListTableAndInvalidScope(t *testing.T) {
	_, _, err := runWaitingRoomCLI(t, "http://example.invalid",
		"waiting-room", "list", "--scope", "global", "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "zone or account") {
		t.Fatalf("error = %v", err)
	}

	// Zone flag with account scope rejected before network.
	_, _, err = runWaitingRoomCLI(t, "http://example.invalid",
		"waiting-room", "list", "--scope", "account", "--zone", "example.com", "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "--zone requires") {
		t.Fatalf("error = %v", err)
	}
}

func TestWaitingRoomListPerPageIsMultipleOfFive(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":[],"result_info":{"page":1,"per_page":100,"total_pages":1,"count":0,"total_count":0}}`))
	}))
	defer srv.Close()

	_, _, err := runWaitingRoomCLI(t, srv.URL, "waiting-room", "list", "--output", "json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotQuery, "per_page=100") {
		t.Fatalf("query = %q, want per_page=100 (multiple of 5)", gotQuery)
	}
}

func TestWaitingRoomResolveZoneInteractiveRequiresZone(t *testing.T) {
	waitingRoomIsolateConfig(t)
	// Non-TTY + dry-run + no configured zone: resolveZoneInteractive must
	// refuse before any product request is built.
	_, _, err := runWaitingRoomCLIOpts(t, "http://example.invalid", false,
		"waiting-room", "get", waitingRoomTestRoomID, "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "no zone specified") {
		t.Fatalf("error = %v, want no zone specified", err)
	}
	if !strings.Contains(err.Error(), "run interactively") && !strings.Contains(err.Error(), "--zone") {
		t.Fatalf("error = %v, want interactive guidance", err)
	}

	// Nested resource path uses the same resolver.
	_, _, err = runWaitingRoomCLIOpts(t, "http://example.invalid", false,
		"waiting-room", "events", "list", waitingRoomTestRoomID, "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "no zone specified") {
		t.Fatalf("events list error = %v, want no zone specified", err)
	}

	// Zone-scoped list (default scope) also uses resolveZoneInteractive.
	_, _, err = runWaitingRoomCLIOpts(t, "http://example.invalid", false,
		"waiting-room", "list", "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "no zone specified") {
		t.Fatalf("list error = %v, want no zone specified", err)
	}
}

func TestWaitingRoomResolveZoneInteractiveNameLookup(t *testing.T) {
	waitingRoomIsolateConfig(t)
	var sawLookup bool
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && r.URL.Path == "/zones":
			sawLookup = true
			if name := r.URL.Query().Get("name"); name != "example.com" {
				t.Errorf("lookup name = %q", name)
			}
			_, _ = w.Write([]byte(`{"success":true,"result":[{"id":"` + waitingRoomTestZoneID + `","name":"example.com"}]}`))
		case r.Method == "GET" && r.URL.Path == "/zones/"+waitingRoomTestZoneID+"/waiting_rooms/"+waitingRoomTestRoomID:
			gotPath = r.URL.Path
			_, _ = w.Write([]byte(`{"success":true,"result":{"id":"` + waitingRoomTestRoomID + `","name":"checkout"}}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	stdout, _, err := runWaitingRoomCLIOpts(t, srv.URL, false,
		"waiting-room", "get", waitingRoomTestRoomID,
		"--zone", "example.com", "--output", "json")
	if err != nil {
		t.Fatal(err)
	}
	if !sawLookup {
		t.Fatal("expected zone name lookup via resolveZoneInteractive")
	}
	if gotPath != "/zones/"+waitingRoomTestZoneID+"/waiting_rooms/"+waitingRoomTestRoomID {
		t.Fatalf("path = %s", gotPath)
	}
	if !strings.Contains(stdout, waitingRoomTestRoomID) {
		t.Fatalf("stdout = %q", stdout)
	}
}

func TestWaitingRoomListResolveZoneInteractiveNameLookup(t *testing.T) {
	waitingRoomIsolateConfig(t)
	var sawLookup bool
	var listPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && r.URL.Path == "/zones":
			sawLookup = true
			_, _ = w.Write([]byte(`{"success":true,"result":[{"id":"` + waitingRoomTestZoneID + `","name":"example.com"}]}`))
		case r.Method == "GET" && r.URL.Path == "/zones/"+waitingRoomTestZoneID+"/waiting_rooms":
			listPath = r.URL.Path
			_, _ = w.Write([]byte(`{"success":true,"result":[],"result_info":{"page":1,"per_page":100,"total_pages":1,"count":0,"total_count":0}}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	if _, _, err := runWaitingRoomCLIOpts(t, srv.URL, false,
		"waiting-room", "list", "--zone", "example.com", "--output", "json"); err != nil {
		t.Fatal(err)
	}
	if !sawLookup {
		t.Fatal("expected zone name lookup for list --scope zone")
	}
	if listPath != "/zones/"+waitingRoomTestZoneID+"/waiting_rooms" {
		t.Fatalf("list path = %s", listPath)
	}
}

func TestWaitingRoomLocalValidationBeforeZoneResolution(t *testing.T) {
	waitingRoomIsolateConfig(t)
	// Invalid create input must fail without needing a zone / client traffic.
	_, _, err := runWaitingRoomCLIOpts(t, "http://example.invalid", false,
		"waiting-room", "create",
		"--name", "r", "--host", "shop.example.com",
		"--new-users-per-minute", "199", "--total-active-users", "200",
		"--dry-run")
	if err == nil || !strings.Contains(err.Error(), "between 200 and") {
		t.Fatalf("error = %v, want user bound error before zone resolve", err)
	}
}
