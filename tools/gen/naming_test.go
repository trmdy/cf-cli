package main

import "testing"

func TestDeriveCommand(t *testing.T) {
	m := mapping{
		Overrides: map[string]string{
			"zone:dns_records": "dns-records",
			"zone:purge_cache": "cache",
		},
		OpOverrides: map[string]opOverride{
			"POST /zones/{zone_id}/purge_cache": {Product: "cache", Name: "purge"},
			"GET /user":                         {Name: "get"},
		},
	}

	cases := []struct {
		method, path string
		hasGet       bool
		product      string
		name         string
		scope        string
	}{
		{"get", "/zones/{zone_id}/dns_records", true, "dns-records", "list", "zone"},
		{"post", "/zones/{zone_id}/dns_records", true, "dns-records", "create", "zone"},
		{"get", "/zones/{zone_id}/dns_records/{dns_record_id}", true, "dns-records", "get", "zone"},
		{"patch", "/zones/{zone_id}/dns_records/{dns_record_id}", true, "dns-records", "update", "zone"},
		{"put", "/zones/{zone_id}/dns_records/{dns_record_id}", true, "dns-records", "replace", "zone"},
		{"delete", "/zones/{zone_id}/dns_records/{dns_record_id}", true, "dns-records", "delete", "zone"},
		{"post", "/zones/{zone_id}/dns_records/import", false, "dns-records", "import", "zone"},
		{"post", "/zones/{zone_id}/purge_cache", true, "cache", "purge", "zone"},
		{"get", "/zones", true, "zones", "list", "root"},
		{"get", "/zones/{zone_id}", true, "zones", "get", "zone"},
		{"get", "/accounts/{account_id}", true, "accounts", "get", "account"},
		{"get", "/user", true, "user", "get", "root"},
		{"get", "/accounts/{account_id}/r2/buckets", true, "r2", "buckets-list", "account"},
		{"put", "/accounts/{account_id}/storage/kv/namespaces/{namespace_id}/values/{key_name}", true, "storage", "kv-namespaces-values-replace", "account"},
		// POST with no GET sibling reads as an action verb...
		{"post", "/accounts/{account_id}/workers/scripts/{script_name}/subdomain", false, "workers", "scripts-subdomain", "account"},
		// ...but POST alongside GET is an ordinary create.
		{"post", "/accounts/{account_id}/workers/dispatch/namespaces", true, "workers", "dispatch-namespaces-create", "account"},
		{"get", "/radar/http/summary/{dimension}", true, "radar", "http-summary-get", "root"},
	}
	for _, c := range cases {
		d := deriveCommand(c.method, c.path, c.hasGet, m)
		if d.product != c.product || d.name != c.name || d.scope != c.scope {
			t.Errorf("%s %s: got (%s, %s, %s), want (%s, %s, %s)",
				c.method, c.path, d.product, d.name, d.scope, c.product, c.name, c.scope)
		}
	}
}

func TestSlugSeg(t *testing.T) {
	if slugSeg("DNS_Records") != "dns-records" {
		t.Errorf("slugSeg failed: %s", slugSeg("DNS_Records"))
	}
}
