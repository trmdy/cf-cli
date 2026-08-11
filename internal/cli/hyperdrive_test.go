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

const hyperdriveTestAccountID = "023e105f4ecef8ad9ca31a8372d0c353"

func runHyperdriveCLI(t *testing.T, serverURL string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := NewRootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs(append([]string{"--base-url", serverURL, "--token", "test-token", "--account-id", hyperdriveTestAccountID}, args...))
	err = root.Execute()
	return out.String(), errBuf.String(), err
}

func TestHyperdriveCreateDryRunBuildsPublicOrigin(t *testing.T) {
	stdout, _, err := runHyperdriveCLI(t, "http://example.invalid",
		"hyperdrive", "create", "app-db",
		"--host", "db.example.com",
		"--database", "app",
		"--user", "app-user",
		"--password", "secret",
		"--scheme", "POSTGRESQL",
		"--port", "5432",
		"--access-client-id", "access-id",
		"--access-client-secret", "access-secret",
		"--cache-max-age", "120",
		"--stale-while-revalidate", "30",
		"--ca-certificate-id", "ca-id",
		"--mtls-certificate-id", "cert-id",
		"--sslmode", " VERIFY-FULL ",
		"--connection-limit", "100",
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
	if dump.Method != "POST" || !strings.HasSuffix(dump.URL, "/accounts/"+hyperdriveTestAccountID+"/hyperdrive/configs") {
		t.Fatalf("request = %s %s", dump.Method, dump.URL)
	}
	hyperdriveAssertJSONEqual(t, dump.Body, `{
		"name":"app-db",
		"origin":{"host":"db.example.com","database":"app","user":"app-user","password":"secret","scheme":"postgresql","port":5432,"access_client_id":"access-id","access_client_secret":"access-secret"},
		"caching":{"max_age":120,"stale_while_revalidate":30},
		"mtls":{"ca_certificate_id":"ca-id","mtls_certificate_id":"cert-id","sslmode":"verify-full"},
		"origin_connection_limit":100
	}`)
}

func TestHyperdriveCreateDryRunBuildsVPCOrigin(t *testing.T) {
	stdout, _, err := runHyperdriveCLI(t, "http://example.invalid",
		"hyperdrive", "create", "vpc-db",
		"--service-id", "service-123",
		"--database", "app",
		"--user", "app-user",
		"--password", "secret",
		"--scheme", "mysql",
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
	hyperdriveAssertJSONEqual(t, dump.Body, `{"name":"vpc-db","origin":{"service_id":"service-123","database":"app","user":"app-user","password":"secret","scheme":"mysql"}}`)
}

func TestHyperdriveCreateValidation(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "missing origin fields",
			args: []string{"hyperdrive", "create", "app-db", "--host", "db.example.com", "--dry-run"},
			want: "--scheme",
		},
		{
			name: "unpaired access credentials",
			args: []string{"hyperdrive", "create", "app-db", "--host", "db.example.com", "--database", "app", "--user", "user", "--password", "secret", "--scheme", "postgres", "--access-client-id", "id", "--dry-run"},
			want: "provided together",
		},
		{
			name: "vpc with mtls",
			args: []string{"hyperdrive", "create", "app-db", "--service-id", "service", "--database", "app", "--user", "user", "--password", "secret", "--scheme", "postgres", "--sslmode", "require", "--dry-run"},
			want: "cannot be used",
		},
		{
			name: "invalid scheme",
			args: []string{"hyperdrive", "create", "app-db", "--host", "db.example.com", "--database", "app", "--user", "user", "--password", "secret", "--scheme", "oracle", "--dry-run"},
			want: "--scheme",
		},
		{
			name: "connection limit above maximum",
			args: []string{"hyperdrive", "create", "app-db", "--host", "db.example.com", "--database", "app", "--user", "user", "--password", "secret", "--scheme", "postgres", "--connection-limit", "101", "--dry-run"},
			want: "between 5 and 100",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := runHyperdriveCLI(t, "http://example.invalid", tc.args...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestHyperdriveUpdateDryRunBuildsPatch(t *testing.T) {
	stdout, _, err := runHyperdriveCLI(t, "http://example.invalid",
		"hyperdrive", "update", "config/id",
		"--name", "renamed",
		"--host", "db2.example.com",
		"--password", "new-secret",
		"--caching-disabled=false",
		"--cache-max-age", "90",
		"--sslmode", " VERIFY-CA ",
		"--connection-limit", "100",
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
	if dump.Method != "PATCH" || !strings.HasSuffix(dump.URL, "/hyperdrive/configs/config%2Fid") {
		t.Fatalf("request = %s %s", dump.Method, dump.URL)
	}
	hyperdriveAssertJSONEqual(t, dump.Body, `{"name":"renamed","origin":{"host":"db2.example.com","password":"new-secret"},"caching":{"disabled":false,"max_age":90},"mtls":{"sslmode":"verify-ca"},"origin_connection_limit":100}`)
}

func TestHyperdriveUpdateValidation(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"no fields", []string{"hyperdrive", "update", "id", "--dry-run"}, "nothing to update"},
		{"invalid limit", []string{"hyperdrive", "update", "id", "--connection-limit", "4", "--dry-run"}, "between 5 and 100"},
		{"limit above maximum", []string{"hyperdrive", "update", "id", "--connection-limit", "101", "--dry-run"}, "between 5 and 100"},
		{"invalid tls", []string{"hyperdrive", "update", "id", "--sslmode", "invalid", "--dry-run"}, "--sslmode"},
		{"vpc and host", []string{"hyperdrive", "update", "id", "--service-id", "service", "--host", "host", "--dry-run"}, "cannot be updated"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := runHyperdriveCLI(t, "http://example.invalid", tc.args...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestHyperdriveHTTPCommands(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		wantMethod string
		wantPath   string
		wantBody   string
		response   string
	}{
		{
			name:       "list",
			args:       []string{"hyperdrive", "list", "--output", "json"},
			wantMethod: "GET",
			wantPath:   "/accounts/" + hyperdriveTestAccountID + "/hyperdrive/configs",
			response:   `{"success":true,"result":[{"id":"config-1","name":"app-db","origin":{"host":"db.example.com","database":"app"}}]}`,
		},
		{
			name:       "get",
			args:       []string{"hyperdrive", "get", "config-1"},
			wantMethod: "GET",
			wantPath:   "/accounts/" + hyperdriveTestAccountID + "/hyperdrive/configs/config-1",
			response:   `{"success":true,"result":{"id":"config-1"}}`,
		},
		{
			name:       "create",
			args:       []string{"hyperdrive", "create", "app-db", "--host", "db.example.com", "--database", "app", "--user", "user", "--password", "secret", "--scheme", "postgres"},
			wantMethod: "POST",
			wantPath:   "/accounts/" + hyperdriveTestAccountID + "/hyperdrive/configs",
			wantBody:   `{"name":"app-db","origin":{"host":"db.example.com","database":"app","user":"user","password":"secret","scheme":"postgres"}}`,
			response:   `{"success":true,"result":{"id":"config-1"}}`,
		},
		{
			name:       "update",
			args:       []string{"hyperdrive", "update", "config-1", "--name", "renamed"},
			wantMethod: "PATCH",
			wantPath:   "/accounts/" + hyperdriveTestAccountID + "/hyperdrive/configs/config-1",
			wantBody:   `{"name":"renamed"}`,
			response:   `{"success":true,"result":{"id":"config-1","name":"renamed"}}`,
		},
		{
			name:       "delete",
			args:       []string{"hyperdrive", "delete", "config-1", "--force"},
			wantMethod: "DELETE",
			wantPath:   "/accounts/" + hyperdriveTestAccountID + "/hyperdrive/configs/config-1",
			response:   `{"success":true,"result":{}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotBody []byte
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != tc.wantMethod || r.URL.Path != tc.wantPath {
					t.Errorf("request = %s %s, want %s %s", r.Method, r.URL.Path, tc.wantMethod, tc.wantPath)
				}
				if r.URL.Query().Get("per_page") != "" && r.URL.Query().Get("per_page") != "100" {
					t.Errorf("per_page = %q", r.URL.Query().Get("per_page"))
				}
				gotBody, _ = io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.response))
			}))
			defer srv.Close()

			if _, _, err := runHyperdriveCLI(t, srv.URL, tc.args...); err != nil {
				t.Fatal(err)
			}
			if tc.wantBody != "" {
				hyperdriveAssertJSONEqual(t, gotBody, tc.wantBody)
			}
		})
	}
}

func TestHyperdriveDeleteRequiresForceWithoutTTY(t *testing.T) {
	_, _, err := runHyperdriveCLI(t, "http://example.invalid", "hyperdrive", "delete", "config-1")
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("error = %v", err)
	}
}

func TestHyperdriveListTableAndHelp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":[{"id":"config-1","name":"app-db","origin":{"host":"db.example.com","database":"app"},"caching":{"disabled":false},"origin_connection_limit":60}]}`))
	}))
	defer srv.Close()
	stdout, _, err := runHyperdriveCLI(t, srv.URL, "hyperdrive", "list")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ID", "NAME", "ORIGIN", "app-db", "db.example.com/app"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("list output missing %q: %s", want, stdout)
		}
	}

	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"hyperdrive", "create", "--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"cf hyperdrive create", "--host", "--service-id", "--connection-limit"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("help missing %q: %s", want, out.String())
		}
	}
}

func hyperdriveAssertJSONEqual(t *testing.T, got []byte, want string) {
	t.Helper()
	var gotValue, wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("got invalid JSON %s: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatalf("want invalid JSON %s: %v", want, err)
	}
	gotJSON, _ := json.Marshal(gotValue)
	wantJSON, _ := json.Marshal(wantValue)
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("JSON = %s, want %s", gotJSON, wantJSON)
	}
}
