package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

const devicesFleetTestAccountID = "0123456789abcdef0123456789abcdef"

func runDevicesFleetCLI(t *testing.T, serverURL string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := NewRootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	all := append([]string{
		"--base-url", serverURL,
		"--token", "test-token",
		"--account-id", devicesFleetTestAccountID,
	}, args...)
	root.SetArgs(all)
	err = root.Execute()
	return out.String(), errBuf.String(), err
}

func devicesFleetAssertJSONEqual(t *testing.T, got []byte, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("got invalid JSON %s: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("want invalid JSON %s: %v", want, err)
	}
	gb, _ := json.Marshal(g)
	wb, _ := json.Marshal(w)
	if string(gb) != string(wb) {
		t.Fatalf("json = %s, want %s", gb, wb)
	}
}

// devicesFleetDump is the --dry-run request representation.
type devicesFleetDump struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Body    json.RawMessage   `json:"body"`
}

func devicesFleetParseDump(t *testing.T, stdout string) devicesFleetDump {
	t.Helper()
	var d devicesFleetDump
	if err := json.Unmarshal([]byte(stdout), &d); err != nil {
		t.Fatalf("dry-run output not JSON: %v\n%s", err, stdout)
	}
	return d
}

// devicesFleetChanged builds the flag-changed predicate the body builder uses.
func devicesFleetChanged(flags ...string) func(string) bool {
	set := make(map[string]bool, len(flags))
	for _, f := range flags {
		set[f] = true
	}
	return func(flag string) bool { return set[flag] }
}

// --- device list query -----------------------------------------------------

func TestBuildDevicesFleetListQueryEmpty(t *testing.T) {
	q, err := buildDevicesFleetListQuery(devicesFleetListOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(q) != 0 {
		t.Fatalf("query = %v, want empty", q)
	}
}

func TestBuildDevicesFleetListQueryAllFilters(t *testing.T) {
	q, err := buildDevicesFleetListQuery(devicesFleetListOpts{
		search:              "macbook",
		userEmail:           "alice@example.com",
		activeRegistrations: "include",
		seenAfter:           "2026-01-01T00:00:00Z",
		seenBefore:          "2026-02-01T00:00:00Z",
		sortBy:              "last_seen_at",
		sortOrder:           "desc",
		perPage:             50,
		perPageSet:          true,
		withProfile:         true,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := url.Values{
		"search":               {"macbook"},
		"last_seen_user.email": {"alice@example.com"},
		"active_registrations": {"include"},
		"seen_after":           {"2026-01-01T00:00:00Z"},
		"seen_before":          {"2026-02-01T00:00:00Z"},
		"sort_by":              {"last_seen_at"},
		"sort_order":           {"desc"},
		"per_page":             {"50"},
		"include":              {"last_seen_registration.policy"},
	}
	if q.Encode() != want.Encode() {
		t.Fatalf("query = %s, want %s", q.Encode(), want.Encode())
	}
}

func TestBuildDevicesFleetListQueryCanonicalizesEnums(t *testing.T) {
	q, err := buildDevicesFleetListQuery(devicesFleetListOpts{
		activeRegistrations: "EXCLUDE",
		sortBy:              "Last_Seen_User.Email",
		sortOrder:           "ASC",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := q.Get("active_registrations"); got != "exclude" {
		t.Fatalf("active_registrations = %q, want exclude", got)
	}
	if got := q.Get("sort_by"); got != "last_seen_user.email" {
		t.Fatalf("sort_by = %q, want last_seen_user.email", got)
	}
	if got := q.Get("sort_order"); got != "asc" {
		t.Fatalf("sort_order = %q, want asc", got)
	}
}

func TestBuildDevicesFleetListQueryAcceptsEveryDocumentedEnumValue(t *testing.T) {
	for _, v := range devicesFleetSortFields {
		if _, err := buildDevicesFleetListQuery(devicesFleetListOpts{sortBy: v}); err != nil {
			t.Fatalf("sort-by %q rejected: %v", v, err)
		}
	}
	for _, v := range devicesFleetSortOrders {
		if _, err := buildDevicesFleetListQuery(devicesFleetListOpts{sortOrder: v}); err != nil {
			t.Fatalf("sort-order %q rejected: %v", v, err)
		}
	}
	for _, v := range devicesFleetRegistrationFilters {
		if _, err := buildDevicesFleetListQuery(devicesFleetListOpts{activeRegistrations: v}); err != nil {
			t.Fatalf("active-registrations %q rejected: %v", v, err)
		}
	}
}

func TestBuildDevicesFleetListQueryValidation(t *testing.T) {
	tests := []struct {
		name string
		opts devicesFleetListOpts
		want string
	}{
		{"bad registration filter", devicesFleetListOpts{activeRegistrations: "all"}, "unknown --active-registrations"},
		{"bad sort field", devicesFleetListOpts{sortBy: "hostname"}, "unknown --sort-by"},
		{"bad sort order", devicesFleetListOpts{sortOrder: "descending"}, "unknown --sort-order"},
		{"zero per page", devicesFleetListOpts{perPage: 0, perPageSet: true}, "--per-page must be a positive"},
		{"negative per page", devicesFleetListOpts{perPage: -3, perPageSet: true}, "--per-page must be a positive"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := buildDevicesFleetListQuery(tt.opts)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestBuildDevicesFleetListQueryOmitsUnsetPerPage(t *testing.T) {
	q, err := buildDevicesFleetListQuery(devicesFleetListOpts{perPage: 0})
	if err != nil {
		t.Fatal(err)
	}
	if q.Has("per_page") {
		t.Fatalf("per_page = %q, want unset", q.Get("per_page"))
	}
}

// --- profile bodies --------------------------------------------------------

func TestBuildDevicesFleetProfileBodyCreateMinimal(t *testing.T) {
	body, err := buildDevicesFleetProfileBody(devicesFleetProfileOpts{
		name:       "Contractors",
		match:      `identity.groups.name == "contractors"`,
		precedence: 100,
		changed:    devicesFleetChanged("name", "match", "precedence"),
	}, true, false)
	if err != nil {
		t.Fatal(err)
	}
	devicesFleetAssertJSONEqual(t, body, `{"name":"Contractors","match":"identity.groups.name == \"contractors\"","precedence":100}`)
}

func TestBuildDevicesFleetProfileBodyCreateWithSettings(t *testing.T) {
	body, err := buildDevicesFleetProfileBody(devicesFleetProfileOpts{
		name:               "Kiosks",
		match:              `os.name == "windows"`,
		precedence:         50,
		description:        "Locked-down kiosks",
		enabled:            false,
		serviceMode:        "proxy",
		proxyPort:          3128,
		autoConnect:        900,
		captivePortal:      180,
		lanAllowMinutes:    30,
		lanAllowSubnetSize: 24,
		switchLocked:       true,
		allowUpdates:       true,
		supportURL:         "https://support.example.com",
		tunnelProtocol:     "masque",
		changed: devicesFleetChanged("name", "match", "precedence", "description", "enabled",
			"service-mode", "proxy-port", "auto-connect", "captive-portal", "lan-allow-minutes",
			"lan-allow-subnet-size", "switch-locked", "allow-updates", "support-url", "tunnel-protocol"),
	}, true, false)
	if err != nil {
		t.Fatal(err)
	}
	devicesFleetAssertJSONEqual(t, body, `{
		"name":"Kiosks",
		"match":"os.name == \"windows\"",
		"precedence":50,
		"description":"Locked-down kiosks",
		"enabled":false,
		"service_mode_v2":{"mode":"proxy","port":3128},
		"auto_connect":900,
		"captive_portal":180,
		"lan_allow_minutes":30,
		"lan_allow_subnet_size":24,
		"switch_locked":true,
		"allow_updates":true,
		"support_url":"https://support.example.com",
		"tunnel_protocol":"masque"
	}`)
}

func TestBuildDevicesFleetProfileBodyCreateOmitsUnsetSettings(t *testing.T) {
	// enabled defaults to true on the create command's flag set; leaving it
	// unset must not send it, so the API default stands.
	body, err := buildDevicesFleetProfileBody(devicesFleetProfileOpts{
		name:            "Default-ish",
		match:           "os.name == \"mac\"",
		precedence:      10,
		enabled:         true,
		switchLocked:    true,
		autoConnect:     0,
		lanAllowMinutes: 5,
		changed:         devicesFleetChanged("name", "match", "precedence"),
	}, true, false)
	if err != nil {
		t.Fatal(err)
	}
	devicesFleetAssertJSONEqual(t, body, `{"name":"Default-ish","match":"os.name == \"mac\"","precedence":10}`)
}

func TestBuildDevicesFleetProfileBodyCreateKeepsExplicitZeroAndFalse(t *testing.T) {
	body, err := buildDevicesFleetProfileBody(devicesFleetProfileOpts{
		name:            "Zeroes",
		match:           "os.name == \"linux\"",
		precedence:      0,
		enabled:         false,
		autoConnect:     0,
		lanAllowMinutes: 0,
		allowedToLeave:  false,
		changed: devicesFleetChanged("name", "match", "precedence", "enabled",
			"auto-connect", "lan-allow-minutes", "allowed-to-leave"),
	}, true, false)
	if err != nil {
		t.Fatal(err)
	}
	devicesFleetAssertJSONEqual(t, body, `{
		"name":"Zeroes","match":"os.name == \"linux\"","precedence":0,
		"enabled":false,"auto_connect":0,"lan_allow_minutes":0,"allowed_to_leave":false
	}`)
}

func TestBuildDevicesFleetProfileBodyCreateValidation(t *testing.T) {
	tests := []struct {
		name string
		opts devicesFleetProfileOpts
		want string
	}{
		{
			"missing name",
			devicesFleetProfileOpts{match: "os.name == \"mac\"", changed: devicesFleetChanged("match", "precedence")},
			"--name is required",
		},
		{
			"blank name",
			devicesFleetProfileOpts{name: "   ", match: "os.name == \"mac\"", changed: devicesFleetChanged("name", "match", "precedence")},
			"--name is required",
		},
		{
			"name too long",
			devicesFleetProfileOpts{name: strings.Repeat("n", devicesFleetMaxProfileName+1), match: "os.name == \"mac\"", changed: devicesFleetChanged("name", "match", "precedence")},
			"--name must be at most 100 characters",
		},
		{
			"missing match",
			devicesFleetProfileOpts{name: "X", changed: devicesFleetChanged("name", "precedence")},
			"--match is required",
		},
		{
			"match too long",
			devicesFleetProfileOpts{name: "X", match: strings.Repeat("m", devicesFleetMaxMatch+1), changed: devicesFleetChanged("name", "match", "precedence")},
			"--match must be at most 500 characters",
		},
		{
			"missing precedence",
			devicesFleetProfileOpts{name: "X", match: "os.name == \"mac\"", changed: devicesFleetChanged("name", "match")},
			"--precedence is required",
		},
		{
			"description too long",
			devicesFleetProfileOpts{
				name: "X", match: "os.name == \"mac\"",
				description: strings.Repeat("d", devicesFleetMaxDescription+1),
				changed:     devicesFleetChanged("name", "match", "precedence", "description"),
			},
			"--description must be at most 500 characters",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := buildDevicesFleetProfileBody(tt.opts, true, false)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestBuildDevicesFleetProfileBodyAcceptsMaximumLengths(t *testing.T) {
	body, err := buildDevicesFleetProfileBody(devicesFleetProfileOpts{
		name:        strings.Repeat("n", devicesFleetMaxProfileName),
		match:       strings.Repeat("m", devicesFleetMaxMatch),
		description: strings.Repeat("d", devicesFleetMaxDescription),
		precedence:  1,
		changed:     devicesFleetChanged("name", "match", "precedence", "description"),
	}, true, false)
	if err != nil {
		t.Fatalf("maximum-length values rejected: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if len(got["name"].(string)) != devicesFleetMaxProfileName {
		t.Fatalf("name length = %d", len(got["name"].(string)))
	}
}

// TestBuildDevicesFleetProfileBodyCountsUnicodeCodePoints pins that maxLength
// is counted the way JSON Schema counts it — in code points, not UTF-8 bytes.
// Every value below is multibyte: "é" is 2 bytes, "設" 3, and "😀" 4.
func TestBuildDevicesFleetProfileBodyCountsUnicodeCodePoints(t *testing.T) {
	tests := []struct {
		name  string
		rune  string
		field string
		max   int
	}{
		{"name 2-byte", "é", "name", devicesFleetMaxProfileName},
		{"name 3-byte", "設", "name", devicesFleetMaxProfileName},
		{"name 4-byte", "😀", "name", devicesFleetMaxProfileName},
		{"match 3-byte", "設", "match", devicesFleetMaxMatch},
		{"match 4-byte", "😀", "match", devicesFleetMaxMatch},
		{"description 3-byte", "設", "description", devicesFleetMaxDescription},
		{"description 4-byte", "😀", "description", devicesFleetMaxDescription},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exact := strings.Repeat(tt.rune, tt.max)
			over := strings.Repeat(tt.rune, tt.max+1)
			if len(exact) <= tt.max {
				t.Fatalf("test value is not multibyte: %d bytes for %d code points", len(exact), tt.max)
			}

			opts := func(v string) devicesFleetProfileOpts {
				o := devicesFleetProfileOpts{
					name:       "N",
					match:      "os.name == \"mac\"",
					precedence: 1,
					changed:    devicesFleetChanged("name", "match", "precedence"),
				}
				switch tt.field {
				case "name":
					o.name = v
				case "match":
					o.match = v
				case "description":
					o.description = v
					o.changed = devicesFleetChanged("name", "match", "precedence", "description")
				}
				return o
			}

			// Exactly at the bound: accepted, and the value survives intact.
			body, err := buildDevicesFleetProfileBody(opts(exact), true, false)
			if err != nil {
				t.Fatalf("%d code points rejected: %v", tt.max, err)
			}
			var got map[string]any
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatal(err)
			}
			if got[tt.field] != exact {
				t.Fatalf("%s = %v, want the exact-length value round-tripped", tt.field, got[tt.field])
			}

			// One code point over: rejected, and the error reports code points.
			_, err = buildDevicesFleetProfileBody(opts(over), true, false)
			wantMsg := fmt.Sprintf("--%s must be at most %d characters, got %d", tt.field, tt.max, tt.max+1)
			if err == nil || !strings.Contains(err.Error(), wantMsg) {
				t.Fatalf("err = %v, want containing %q", err, wantMsg)
			}
		})
	}
}

func TestBuildDevicesFleetProfileBodyUpdateCountsUnicodeCodePoints(t *testing.T) {
	exact := strings.Repeat("😀", devicesFleetMaxProfileName)
	over := strings.Repeat("😀", devicesFleetMaxProfileName+1)

	body, err := buildDevicesFleetProfileBody(devicesFleetProfileOpts{
		name: exact, changed: devicesFleetChanged("name"),
	}, false, false)
	if err != nil {
		t.Fatalf("%d code points rejected on update: %v", devicesFleetMaxProfileName, err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got["name"] != exact {
		t.Fatalf("name = %v, want the exact-length value", got["name"])
	}

	_, err = buildDevicesFleetProfileBody(devicesFleetProfileOpts{
		name: over, changed: devicesFleetChanged("name"),
	}, false, false)
	if err == nil || !strings.Contains(err.Error(), "got 101") {
		t.Fatalf("err = %v, want a 101-code-point rejection", err)
	}
}

func TestDevicesFleetValidateProfileIDCountsUnicodeCodePoints(t *testing.T) {
	if err := devicesFleetValidateProfileID(strings.Repeat("設", devicesFleetMaxProfileID)); err != nil {
		t.Fatalf("%d multibyte code points rejected: %v", devicesFleetMaxProfileID, err)
	}
	err := devicesFleetValidateProfileID(strings.Repeat("設", devicesFleetMaxProfileID+1))
	if err == nil || !strings.Contains(err.Error(), "at most 36 characters, got 37") {
		t.Fatalf("err = %v, want a 37-code-point rejection", err)
	}
}

func TestBuildDevicesFleetProfileBodyUpdateCustom(t *testing.T) {
	body, err := buildDevicesFleetProfileBody(devicesFleetProfileOpts{
		precedence:   20,
		switchLocked: false,
		changed:      devicesFleetChanged("precedence", "switch-locked"),
	}, false, false)
	if err != nil {
		t.Fatal(err)
	}
	devicesFleetAssertJSONEqual(t, body, `{"precedence":20,"switch_locked":false}`)
}

func TestBuildDevicesFleetProfileBodyUpdateDefaultRejectsCustomOnlyFlags(t *testing.T) {
	for _, flag := range devicesFleetCustomOnlyFlags {
		t.Run(flag, func(t *testing.T) {
			_, err := buildDevicesFleetProfileBody(devicesFleetProfileOpts{
				name:    "X",
				match:   "os.name == \"mac\"",
				changed: devicesFleetChanged(flag),
			}, false, true)
			if err == nil || !strings.Contains(err.Error(), "--"+flag+" applies to custom profiles only") {
				t.Fatalf("err = %v, want custom-only rejection for --%s", err, flag)
			}
		})
	}
}

func TestBuildDevicesFleetProfileBodyUpdateDefaultAllowsSharedSettings(t *testing.T) {
	body, err := buildDevicesFleetProfileBody(devicesFleetProfileOpts{
		captivePortal:      300,
		lanAllowSubnetSize: 24,
		allowModeSwitch:    true,
		supportURL:         "",
		changed:            devicesFleetChanged("captive-portal", "lan-allow-subnet-size", "allow-mode-switch", "support-url"),
	}, false, true)
	if err != nil {
		t.Fatal(err)
	}
	devicesFleetAssertJSONEqual(t, body, `{"captive_portal":300,"lan_allow_subnet_size":24,"allow_mode_switch":true,"support_url":""}`)
}

func TestBuildDevicesFleetProfileBodyUpdateRequiresAFlag(t *testing.T) {
	_, err := buildDevicesFleetProfileBody(devicesFleetProfileOpts{changed: devicesFleetChanged()}, false, false)
	if err == nil || !strings.Contains(err.Error(), "nothing to update") {
		t.Fatalf("err = %v, want nothing-to-update", err)
	}
	if !strings.Contains(err.Error(), "--name") {
		t.Fatalf("custom-profile hint should list --name: %v", err)
	}

	_, err = buildDevicesFleetProfileBody(devicesFleetProfileOpts{changed: devicesFleetChanged()}, false, true)
	if err == nil || !strings.Contains(err.Error(), "nothing to update") {
		t.Fatalf("err = %v, want nothing-to-update", err)
	}
	if strings.Contains(err.Error(), "--name") {
		t.Fatalf("default-profile hint must not list --name: %v", err)
	}
}

func TestBuildDevicesFleetProfileBodyUpdateRejectsBlankIdentityFields(t *testing.T) {
	if _, err := buildDevicesFleetProfileBody(devicesFleetProfileOpts{
		name: "  ", changed: devicesFleetChanged("name"),
	}, false, false); err == nil || !strings.Contains(err.Error(), "--name must not be empty") {
		t.Fatalf("err = %v, want empty-name rejection", err)
	}
	if _, err := buildDevicesFleetProfileBody(devicesFleetProfileOpts{
		match: "", changed: devicesFleetChanged("match"),
	}, false, false); err == nil || !strings.Contains(err.Error(), "--match must not be empty") {
		t.Fatalf("err = %v, want empty-match rejection", err)
	}
}

func TestBuildDevicesFleetProfileBodyServiceMode(t *testing.T) {
	t.Run("mode only replaces the object", func(t *testing.T) {
		body, err := buildDevicesFleetProfileBody(devicesFleetProfileOpts{
			serviceMode: "warp",
			changed:     devicesFleetChanged("service-mode"),
		}, false, true)
		if err != nil {
			t.Fatal(err)
		}
		devicesFleetAssertJSONEqual(t, body, `{"service_mode_v2":{"mode":"warp"}}`)
	})
	// mode and port are independently optional in the pinned PATCH schemas, so
	// either serializes on its own.
	t.Run("port only replaces the object", func(t *testing.T) {
		body, err := buildDevicesFleetProfileBody(devicesFleetProfileOpts{
			proxyPort: 3128,
			changed:   devicesFleetChanged("proxy-port"),
		}, false, true)
		if err != nil {
			t.Fatal(err)
		}
		devicesFleetAssertJSONEqual(t, body, `{"service_mode_v2":{"port":3128}}`)
	})
	t.Run("port only on a custom profile", func(t *testing.T) {
		body, err := buildDevicesFleetProfileBody(devicesFleetProfileOpts{
			proxyPort: 8080,
			changed:   devicesFleetChanged("proxy-port"),
		}, false, false)
		if err != nil {
			t.Fatal(err)
		}
		devicesFleetAssertJSONEqual(t, body, `{"service_mode_v2":{"port":8080}}`)
	})
	t.Run("port only at create", func(t *testing.T) {
		body, err := buildDevicesFleetProfileBody(devicesFleetProfileOpts{
			name: "Proxied", match: `os.name == "mac"`, precedence: 1, proxyPort: 3128,
			changed: devicesFleetChanged("name", "match", "precedence", "proxy-port"),
		}, true, false)
		if err != nil {
			t.Fatal(err)
		}
		devicesFleetAssertJSONEqual(t, body, `{"name":"Proxied","match":"os.name == \"mac\"","precedence":1,"service_mode_v2":{"port":3128}}`)
	})
	t.Run("both fields together", func(t *testing.T) {
		body, err := buildDevicesFleetProfileBody(devicesFleetProfileOpts{
			serviceMode: "proxy", proxyPort: 3128,
			changed: devicesFleetChanged("service-mode", "proxy-port"),
		}, false, true)
		if err != nil {
			t.Fatal(err)
		}
		devicesFleetAssertJSONEqual(t, body, `{"service_mode_v2":{"mode":"proxy","port":3128}}`)
	})
	t.Run("neither flag omits the object", func(t *testing.T) {
		body, err := buildDevicesFleetProfileBody(devicesFleetProfileOpts{
			serviceMode: "proxy", proxyPort: 3128,
			switchLocked: true,
			changed:      devicesFleetChanged("switch-locked"),
		}, false, true)
		if err != nil {
			t.Fatal(err)
		}
		devicesFleetAssertJSONEqual(t, body, `{"switch_locked":true}`)
	})
	t.Run("blank mode rejected", func(t *testing.T) {
		_, err := buildDevicesFleetProfileBody(devicesFleetProfileOpts{
			serviceMode: " ",
			changed:     devicesFleetChanged("service-mode"),
		}, false, true)
		if err == nil || !strings.Contains(err.Error(), "--service-mode must not be empty") {
			t.Fatalf("err = %v, want blank-mode rejection", err)
		}
	})
	t.Run("port zero is sent when explicit", func(t *testing.T) {
		body, err := buildDevicesFleetProfileBody(devicesFleetProfileOpts{
			serviceMode: "proxy",
			proxyPort:   0,
			changed:     devicesFleetChanged("service-mode", "proxy-port"),
		}, false, true)
		if err != nil {
			t.Fatal(err)
		}
		devicesFleetAssertJSONEqual(t, body, `{"service_mode_v2":{"mode":"proxy","port":0}}`)
	})
}

func TestBuildDevicesFleetProfileBodyMapsEverySharedFlag(t *testing.T) {
	opts := devicesFleetProfileOpts{
		autoConnect: 1, captivePortal: 2, lanAllowMinutes: 3, lanAllowSubnetSize: 4,
		allowModeSwitch: true, allowUpdates: true, allowedToLeave: true, switchLocked: true,
		excludeOfficeIPs: true, disableAutoFallback: true, registerInterfaceIPWithDNS: true,
		sccmVPNBoundarySupport: true,
		supportURL:             "https://example.com", tunnelProtocol: "wireguard",
	}
	var flags []string
	for _, f := range devicesFleetSharedIntFlags {
		flags = append(flags, f.flag)
	}
	for _, f := range devicesFleetSharedBoolFlags {
		flags = append(flags, f.flag)
	}
	for _, f := range devicesFleetSharedStringFlags {
		flags = append(flags, f.flag)
	}
	opts.changed = devicesFleetChanged(flags...)
	body, err := buildDevicesFleetProfileBody(opts, false, true)
	if err != nil {
		t.Fatal(err)
	}
	devicesFleetAssertJSONEqual(t, body, `{
		"auto_connect":1,"captive_portal":2,"lan_allow_minutes":3,"lan_allow_subnet_size":4,
		"allow_mode_switch":true,"allow_updates":true,"allowed_to_leave":true,"switch_locked":true,
		"exclude_office_ips":true,"disable_auto_fallback":true,
		"register_interface_ip_with_dns":true,"sccm_vpn_boundary_support":true,
		"support_url":"https://example.com","tunnel_protocol":"wireguard"
	}`)
}

// --- profile ID validation -------------------------------------------------

func TestDevicesFleetValidateProfileID(t *testing.T) {
	if err := devicesFleetValidateProfileID(strings.Repeat("a", devicesFleetMaxProfileID)); err != nil {
		t.Fatalf("36-character ID rejected: %v", err)
	}
	if err := devicesFleetValidateProfileID(strings.Repeat("a", devicesFleetMaxProfileID+1)); err == nil ||
		!strings.Contains(err.Error(), "at most 36 characters") {
		t.Fatalf("err = %v, want length rejection", err)
	}
	if err := devicesFleetValidateProfileID("  "); err == nil || !strings.Contains(err.Error(), "must not be empty") {
		t.Fatalf("err = %v, want empty rejection", err)
	}
}

// --- dry-run requests ------------------------------------------------------

func TestDevicesFleetDryRunRequests(t *testing.T) {
	base := "https://api.example.test/client/v4"
	acct := "/accounts/" + devicesFleetTestAccountID
	tests := []struct {
		name       string
		args       []string
		wantMethod string
		wantURL    string
		wantBody   string
	}{
		{
			name:       "device list",
			args:       []string{"devices", "fleet", "list"},
			wantMethod: "GET",
			wantURL:    base + acct + "/devices/physical-devices",
		},
		{
			name:       "device list filtered",
			args:       []string{"devices", "fleet", "list", "--search", "mac book", "--sort-by", "last_seen_at", "--sort-order", "desc", "--with-profile"},
			wantMethod: "GET",
			wantURL:    base + acct + "/devices/physical-devices?include=last_seen_registration.policy&search=mac+book&sort_by=last_seen_at&sort_order=desc",
		},
		{
			name:       "device get",
			args:       []string{"devices", "fleet", "get", "dev-1"},
			wantMethod: "GET",
			wantURL:    base + acct + "/devices/physical-devices/dev-1",
		},
		{
			name:       "device get with profile",
			args:       []string{"devices", "fleet", "get", "dev-1", "--with-profile"},
			wantMethod: "GET",
			wantURL:    base + acct + "/devices/physical-devices/dev-1?include=last_seen_registration.policy",
		},
		{
			name:       "device revoke",
			args:       []string{"devices", "fleet", "revoke", "dev-1"},
			wantMethod: "POST",
			wantURL:    base + acct + "/devices/physical-devices/dev-1/revoke",
		},
		{
			name:       "profile list",
			args:       []string{"devices", "fleet", "profile", "list"},
			wantMethod: "GET",
			wantURL:    base + acct + "/devices/policies",
		},
		{
			name:       "default profile get",
			args:       []string{"devices", "fleet", "profile", "get"},
			wantMethod: "GET",
			wantURL:    base + acct + "/devices/policy",
		},
		{
			name:       "custom profile get",
			args:       []string{"devices", "fleet", "profile", "get", "pol-1"},
			wantMethod: "GET",
			wantURL:    base + acct + "/devices/policy/pol-1",
		},
		{
			name:       "profile create",
			args:       []string{"devices", "fleet", "profile", "create", "--name", "Contractors", "--match", `identity.groups.name == "contractors"`, "--precedence", "100"},
			wantMethod: "POST",
			wantURL:    base + acct + "/devices/policy",
			wantBody:   `{"name":"Contractors","match":"identity.groups.name == \"contractors\"","precedence":100}`,
		},
		{
			name:       "default profile update",
			args:       []string{"devices", "fleet", "profile", "update", "--switch-locked", "--captive-portal", "180"},
			wantMethod: "PATCH",
			wantURL:    base + acct + "/devices/policy",
			wantBody:   `{"switch_locked":true,"captive_portal":180}`,
		},
		{
			name:       "custom profile update",
			args:       []string{"devices", "fleet", "profile", "update", "pol-1", "--precedence", "20", "--enabled=false"},
			wantMethod: "PATCH",
			wantURL:    base + acct + "/devices/policy/pol-1",
			wantBody:   `{"precedence":20,"enabled":false}`,
		},
		{
			name:       "profile delete",
			args:       []string{"devices", "fleet", "profile", "delete", "pol-1"},
			wantMethod: "DELETE",
			wantURL:    base + acct + "/devices/policy/pol-1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout, _, err := runDevicesFleetCLI(t, base, append(tt.args, "--dry-run")...)
			if err != nil {
				t.Fatal(err)
			}
			d := devicesFleetParseDump(t, stdout)
			if d.Method != tt.wantMethod {
				t.Fatalf("method = %s, want %s", d.Method, tt.wantMethod)
			}
			if d.URL != tt.wantURL {
				t.Fatalf("url = %s, want %s", d.URL, tt.wantURL)
			}
			if tt.wantBody == "" {
				if len(d.Body) != 0 {
					t.Fatalf("body = %s, want none", d.Body)
				}
				return
			}
			devicesFleetAssertJSONEqual(t, d.Body, tt.wantBody)
		})
	}
}

// TestDevicesFleetDryRunSendsNoRequests pins the documented invariant that no
// command in this file reads before it writes.
func TestDevicesFleetDryRunSendsNoRequests(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	runs := [][]string{
		{"devices", "fleet", "list"},
		{"devices", "fleet", "get", "dev-1"},
		{"devices", "fleet", "revoke", "dev-1"},
		{"devices", "fleet", "profile", "list"},
		{"devices", "fleet", "profile", "get"},
		{"devices", "fleet", "profile", "get", "pol-1"},
		{"devices", "fleet", "profile", "create", "--name", "X", "--match", "os.name == \"mac\"", "--precedence", "1"},
		{"devices", "fleet", "profile", "update", "--switch-locked"},
		{"devices", "fleet", "profile", "update", "pol-1", "--precedence", "2"},
		{"devices", "fleet", "profile", "delete", "pol-1"},
	}
	for _, args := range runs {
		if _, _, err := runDevicesFleetCLI(t, srv.URL, append(args, "--dry-run")...); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	if hits != 0 {
		t.Fatalf("dry-run sent %d requests, want 0", hits)
	}
}

func TestDevicesFleetEscapesIDsInPath(t *testing.T) {
	stdout, _, err := runDevicesFleetCLI(t, "https://api.example.test/client/v4",
		"devices", "fleet", "revoke", "dev/../evil", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	d := devicesFleetParseDump(t, stdout)
	if !strings.Contains(d.URL, "/devices/physical-devices/dev%2F..%2Fevil/revoke") {
		t.Fatalf("url = %s, want escaped device ID", d.URL)
	}
}

// --- validation ordering ---------------------------------------------------

func TestDevicesFleetValidatesBeforeAnyNetworkWork(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	tests := []struct {
		name string
		args []string
		want string
	}{
		{"bad sort field", []string{"devices", "fleet", "list", "--sort-by", "hostname"}, "unknown --sort-by"},
		{"bad per page", []string{"devices", "fleet", "list", "--per-page", "0"}, "--per-page must be a positive"},
		{"empty device id", []string{"devices", "fleet", "get", " "}, "device ID must not be empty"},
		{"long profile id", []string{"devices", "fleet", "profile", "get", strings.Repeat("p", 40)}, "at most 36 characters"},
		{"default profile identity flag", []string{"devices", "fleet", "profile", "update", "--name", "X"}, "applies to custom profiles only"},
		{"empty update", []string{"devices", "fleet", "profile", "update", "pol-1"}, "nothing to update"},
		{"blank service mode", []string{"devices", "fleet", "profile", "update", "pol-1", "--service-mode", " "}, "--service-mode must not be empty"},
		{"blank create name", []string{"devices", "fleet", "profile", "create", "--name", " ", "--match", "os.name == \"mac\"", "--precedence", "1"}, "--name is required"},
		{"list rejects positional args", []string{"devices", "fleet", "list", "extra"}, `unknown command "extra"`},
		{"profile list rejects positional args", []string{"devices", "fleet", "profile", "list", "extra"}, `unknown command "extra"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := runDevicesFleetCLI(t, srv.URL, tt.args...)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want containing %q", err, tt.want)
			}
		})
	}
	if hits != 0 {
		t.Fatalf("invalid input sent %d requests, want 0", hits)
	}
}

// TestDevicesFleetValidatesBeforeAccountResolution keeps local input errors
// ahead of the "no account" error, so a typo is reported as a typo.
func TestDevicesFleetValidatesBeforeAccountResolution(t *testing.T) {
	t.Setenv("CF_CONFIG_DIR", t.TempDir())
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "")

	tests := []struct {
		name string
		args []string
		want string
	}{
		{"list", []string{"devices", "fleet", "list", "--sort-order", "descending"}, "unknown --sort-order"},
		{"get", []string{"devices", "fleet", "get", ""}, "device ID must not be empty"},
		{"profile update", []string{"devices", "fleet", "profile", "update", "pol-1"}, "nothing to update"},
		{"profile create", []string{"devices", "fleet", "profile", "create", "--name", "", "--match", "x", "--precedence", "1"}, "--name is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := NewRootCmd()
			var out, errBuf bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&errBuf)
			root.SetArgs(append([]string{"--base-url", "https://api.example.test/client/v4", "--token", "t", "--account-id", ""}, tt.args...))
			err := root.Execute()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestDevicesFleetRequiresAccountID(t *testing.T) {
	t.Setenv("CF_CONFIG_DIR", t.TempDir())
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "")

	for _, args := range [][]string{
		{"devices", "fleet", "list"},
		{"devices", "fleet", "get", "dev-1"},
		{"devices", "fleet", "revoke", "dev-1", "--force"},
		{"devices", "fleet", "profile", "list"},
		{"devices", "fleet", "profile", "get"},
		{"devices", "fleet", "profile", "create", "--name", "X", "--match", "y", "--precedence", "1"},
		{"devices", "fleet", "profile", "update", "--switch-locked"},
		{"devices", "fleet", "profile", "delete", "pol-1", "--force"},
	} {
		root := NewRootCmd()
		var out, errBuf bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&errBuf)
		root.SetArgs(append([]string{"--base-url", "https://api.example.test/client/v4", "--token", "t", "--account-id", ""}, args...))
		err := root.Execute()
		if err == nil || !strings.Contains(err.Error(), "no account specified") {
			t.Fatalf("%v: err = %v, want no-account error", args, err)
		}
	}
}

// --- destructive commands --------------------------------------------------

func TestDevicesFleetDestructiveCommandsRequireForceWithoutTTY(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"errors":[],"messages":[],"result":null}`))
	}))
	defer srv.Close()

	for _, args := range [][]string{
		{"devices", "fleet", "revoke", "dev-1"},
		{"devices", "fleet", "profile", "delete", "pol-1"},
	} {
		_, _, err := runDevicesFleetCLI(t, srv.URL, args...)
		if err == nil || !strings.Contains(err.Error(), "aborted (pass --force to skip confirmation)") {
			t.Fatalf("%v: err = %v, want abort", args, err)
		}
	}
	if hits != 0 {
		t.Fatalf("unconfirmed destructive commands sent %d requests, want 0", hits)
	}
}

// --- HTTP behavior ---------------------------------------------------------

func TestDevicesFleetListPaginatesAndRendersTable(t *testing.T) {
	var cursors []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/accounts/"+devicesFleetTestAccountID+"/devices/physical-devices" {
			t.Errorf("path = %s", r.URL.Path)
		}
		cursors = append(cursors, r.URL.Query().Get("cursor"))
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("cursor") == "" {
			_, _ = w.Write([]byte(`{"success":true,"errors":[],"messages":[],"result":[
				{"id":"dev-1","name":"alice-mbp","device_type":"mac","os_version":"14.5","client_version":"2024.6.1",
				 "last_seen_at":"2026-08-01T10:00:00Z","active_registrations":2,
				 "last_seen_user":{"email":"alice@example.com","name":"Alice"}}
			],"result_info":{"count":1,"per_page":1,"cursor":"next-page"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":true,"errors":[],"messages":[],"result":[
			{"id":"dev-2","name":"bob-thinkpad","device_type":"windows","os_version":"11","client_version":"2024.5.9",
			 "last_seen_at":"2026-07-30T08:00:00Z","active_registrations":0,"last_seen_user":null}
		],"result_info":{"count":1,"per_page":1,"cursor":""}}`))
	}))
	defer srv.Close()

	stdout, _, err := runDevicesFleetCLI(t, srv.URL, "devices", "fleet", "list")
	if err != nil {
		t.Fatal(err)
	}
	if len(cursors) != 2 || cursors[0] != "" || cursors[1] != "next-page" {
		t.Fatalf("cursors = %v, want ['', 'next-page']", cursors)
	}
	for _, want := range []string{"ID", "REGISTRATIONS", "dev-1", "alice@example.com", "mac 14.5", "dev-2", "bob-thinkpad"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("table missing %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "PROFILE") {
		t.Fatalf("PROFILE column shown without --with-profile:\n%s", stdout)
	}
}

func TestDevicesFleetListWithProfileAddsColumn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("include"); got != "last_seen_registration.policy" {
			t.Errorf("include = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"errors":[],"messages":[],"result":[
			{"id":"dev-1","name":"alice-mbp","active_registrations":1,
			 "last_seen_registration":{"policy":{"id":"pol-1","name":"Contractors"}}}
		],"result_info":{"count":1,"per_page":1,"cursor":""}}`))
	}))
	defer srv.Close()

	stdout, _, err := runDevicesFleetCLI(t, srv.URL, "devices", "fleet", "list", "--with-profile")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "PROFILE") || !strings.Contains(stdout, "Contractors") {
		t.Fatalf("table missing profile column:\n%s", stdout)
	}
}

func TestDevicesFleetListHonorsQueryAndOutput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"errors":[],"messages":[],"result":[
			{"id":"dev-1","name":"alice-mbp"}
		],"result_info":{"count":1,"per_page":1,"cursor":""}}`))
	}))
	defer srv.Close()

	stdout, _, err := runDevicesFleetCLI(t, srv.URL, "devices", "fleet", "list", "--query", ".[0].name")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(stdout) != `"alice-mbp"` {
		t.Fatalf("stdout = %q", stdout)
	}
}

func TestDevicesFleetProfileListRendersTableWithoutPagination(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/accounts/"+devicesFleetTestAccountID+"/devices/policies" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.URL.RawQuery != "" {
			t.Errorf("query = %q, want none", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		// result_info carries a page/count block but the endpoint takes no
		// pagination parameters; exactly one request must go out.
		_, _ = w.Write([]byte(`{"success":true,"errors":[],"messages":[],"result":[
			{"policy_id":"pol-default","default":true,"enabled":true},
			{"policy_id":"pol-1","name":"Contractors","default":false,"enabled":false,"precedence":100,"match":"identity.groups.name == \"contractors\""}
		],"result_info":{"count":2,"page":1,"per_page":20,"total_count":2}}`))
	}))
	defer srv.Close()

	stdout, _, err := runDevicesFleetCLI(t, srv.URL, "devices", "fleet", "profile", "list")
	if err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
	for _, want := range []string{"PRECEDENCE", "pol-default", "true", "Contractors", "100"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("table missing %q:\n%s", want, stdout)
		}
	}
}

func TestDevicesFleetProfileUpdateHTTPRequest(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(r.Body)
		gotBody = buf.Bytes()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"errors":[],"messages":[],"result":{"policy_id":"pol-1"}}`))
	}))
	defer srv.Close()

	if _, _, err := runDevicesFleetCLI(t, srv.URL, "devices", "fleet", "profile", "update", "pol-1",
		"--service-mode", "proxy", "--proxy-port", "3128", "--allowed-to-leave=false"); err != nil {
		t.Fatal(err)
	}
	if gotMethod != "PATCH" {
		t.Fatalf("method = %s, want PATCH", gotMethod)
	}
	if gotPath != "/accounts/"+devicesFleetTestAccountID+"/devices/policy/pol-1" {
		t.Fatalf("path = %s", gotPath)
	}
	devicesFleetAssertJSONEqual(t, gotBody, `{"service_mode_v2":{"mode":"proxy","port":3128},"allowed_to_leave":false}`)
}

func TestDevicesFleetRevokeHTTPRequest(t *testing.T) {
	var gotMethod, gotPath string
	var bodyLen int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, bodyLen = r.Method, r.URL.Path, r.ContentLength
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"errors":[],"messages":[],"result":null}`))
	}))
	defer srv.Close()

	if _, _, err := runDevicesFleetCLI(t, srv.URL, "devices", "fleet", "revoke", "dev-1", "--force"); err != nil {
		t.Fatal(err)
	}
	if gotMethod != "POST" {
		t.Fatalf("method = %s, want POST", gotMethod)
	}
	if gotPath != "/accounts/"+devicesFleetTestAccountID+"/devices/physical-devices/dev-1/revoke" {
		t.Fatalf("path = %s", gotPath)
	}
	if bodyLen > 0 {
		t.Fatalf("content-length = %d, want no body", bodyLen)
	}
}

func TestDevicesFleetSurfacesAPIErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":1002,"message":"device not found"}],"messages":[],"result":null}`))
	}))
	defer srv.Close()

	_, _, err := runDevicesFleetCLI(t, srv.URL, "devices", "fleet", "get", "missing")
	if err == nil || !strings.Contains(err.Error(), "device not found") {
		t.Fatalf("err = %v, want API error", err)
	}
}
