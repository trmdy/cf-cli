package cli

import (
	"strings"
	"testing"

	"github.com/trmdy/cf-cli/internal/registry"
)

func vals(str map[string]string, arr map[string][]string, changed map[string]bool) paramValues {
	pv := paramValues{
		flagFor: map[string]string{},
		str:     map[string]*string{},
		arr:     map[string]*[]string{},
		changed: func(f string) bool { return changed[f] },
	}
	for k, v := range str {
		v := v
		pv.str[k] = &v
		pv.flagFor[k] = strings.ReplaceAll(k, "_", "-")
	}
	for k, v := range arr {
		v := v
		pv.arr[k] = &v
		pv.flagFor[k] = strings.ReplaceAll(k, "_", "-")
	}
	return pv
}

var listOp = registry.Operation{
	Method: "GET",
	Path:   "/zones/{zone_id}/dns_records",
	Params: []registry.Param{
		{Name: "zone_id", In: "path", Required: true},
		{Name: "type", In: "query"},
		{Name: "tags", In: "query", Type: "array"},
	},
}

func TestBuildRequestSubstitutesScope(t *testing.T) {
	pv := vals(map[string]string{"type": "A"}, map[string][]string{"tags": {"a", "b"}}, map[string]bool{"type": true})
	req, err := buildRequest(listOp, "", "zone123", pv)
	if err != nil {
		t.Fatal(err)
	}
	if req.Path != "/zones/zone123/dns_records" {
		t.Errorf("path = %s", req.Path)
	}
	if req.Query.Get("type") != "A" {
		t.Errorf("query type missing: %v", req.Query)
	}
	if got := req.Query["tags"]; len(got) != 2 {
		t.Errorf("array query param not repeated: %v", got)
	}
}

func TestBuildRequestMissingZone(t *testing.T) {
	pv := vals(nil, nil, nil)
	_, err := buildRequest(listOp, "", "", pv)
	if err == nil || !strings.Contains(err.Error(), "zone ID") {
		t.Errorf("expected missing zone error, got %v", err)
	}
}

func TestBuildRequestUnchangedQueryOmitted(t *testing.T) {
	pv := vals(map[string]string{"type": ""}, nil, map[string]bool{})
	req, err := buildRequest(listOp, "", "z", pv)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := req.Query["type"]; ok {
		t.Error("unset query param should be omitted")
	}
}

func TestBuildRequestMissingPathParam(t *testing.T) {
	op := registry.Operation{
		Method: "GET",
		Path:   "/accounts/{account_id}/r2/buckets/{bucket_name}",
		Params: []registry.Param{
			{Name: "account_id", In: "path", Required: true},
			{Name: "bucket_name", In: "path", Required: true},
		},
	}
	pv := vals(map[string]string{"bucket_name": ""}, nil, nil)
	_, err := buildRequest(op, "acct", "", pv)
	if err == nil || !strings.Contains(err.Error(), "--bucket-name") {
		t.Errorf("expected missing flag error, got %v", err)
	}
	pv = vals(map[string]string{"bucket_name": "my bucket"}, nil, nil)
	req, err := buildRequest(op, "acct", "", pv)
	if err != nil {
		t.Fatal(err)
	}
	if req.Path != "/accounts/acct/r2/buckets/my%20bucket" {
		t.Errorf("path not escaped: %s", req.Path)
	}
}

func TestBuildBodyFields(t *testing.T) {
	body, err := buildBody("", []string{"name=example.com", "proxied=true", "ttl=300", "meta.source=cli"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"meta":{"source":"cli"},"name":"example.com","proxied":true,"ttl":300}`
	if string(body) != want {
		t.Errorf("body = %s, want %s", body, want)
	}
}

func TestBuildBodyDataAndFieldsConflict(t *testing.T) {
	if _, err := buildBody(`{"a":1}`, []string{"b=2"}, nil); err == nil {
		t.Error("expected conflict error")
	}
}

func TestBuildBodyStdin(t *testing.T) {
	body, err := buildBody("@-", nil, strings.NewReader(`{"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != `{"a":1}` {
		t.Errorf("body = %s", body)
	}
}

func TestBuildBodyInvalidJSON(t *testing.T) {
	if _, err := buildBody(`{oops`, nil, nil); err == nil {
		t.Error("expected invalid JSON error")
	}
}
