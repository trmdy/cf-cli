package cli

// DLP porcelain: the Data Loss Prevention workflows a human actually runs —
// browse profiles, build and maintain custom detection profiles, tune the
// built-in predefined ones, manage Exact Data Match / word-list datasets, and
// control payload logging.
//
// DLP publishes 93 generated operations. This file deliberately covers the
// core set above and nothing else; everything below stays on the generated
// layer, where it is fully usable today:
//
//   - individual entry CRUD .......... cf api dlp entries-create|entries-replace|entries-delete
//   - predefined/integration entries . cf api dlp entries-predefined-*|entries-integration-*
//   - predefined profile config ...... cf api dlp profiles-predefined-config-*
//   - email scanner rules ............ cf api dlp email-rules-*|email-account-mapping-*
//   - data classes and data tags ..... cf api dlp data-classes-*|data-tag-categories-*
//   - sensitivity groups and levels .. cf api dlp sensitivity-groups-*
//   - document fingerprints .......... cf api dlp document-fingerprints-*
//   - custom prompt topics ........... cf api dlp custom-prompt-topics-*
//   - multi-column dataset uploads ... cf api dlp datasets-versions-*
//   - account settings and limits .... cf api dlp settings-*|limits-list
//   - regex pattern validation ....... cf api dlp patterns-validate
//
// Every DLP endpoint is account-scoped, so these commands take the account
// from the global --account-id flag, the environment, or the profile.
//
// See docs/STYLE.md; internal/cli/dns.go is the shape exemplar.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/trmdy/cf-cli/internal/api"
	"github.com/trmdy/cf-cli/internal/output"
)

// dlpMaxAllowedMatchCount bounds allowed_match_count where the write schema
// documents a maximum: custom profile create (dlp_NewCustomProfile) and
// predefined profile update (dlp_PredefinedProfileUpdate). The custom profile
// update schema declares no bound, so none is enforced there.
const dlpMaxAllowedMatchCount = 1000

// dlpConfidenceThresholds are the values dlp_Confidence lists on profile
// *responses*. Every profile write schema types confidence_threshold as a
// plain nullable string, so these are documented in help but not enforced.
var dlpConfidenceThresholds = []string{"low", "medium", "high", "very_high"}

// dlpMaskingLevels is the payload log masking_level enum
// (dlp_PayloadLogMaskingLevel).
var dlpMaskingLevels = []string{"full", "partial", "clear", "default"}

// dlpProfileTypes is the profile discriminator returned by
// GET /dlp/profiles and GET /dlp/profiles/{id}.
var dlpProfileTypes = []string{"custom", "predefined", "integration"}

// dlpProfileReadOnly are fields a profile GET returns that are not part of any
// profile write schema.
var dlpProfileReadOnly = []string{"id", "type", "created_at", "updated_at", "open_access"}

// dlpPredefinedPatchable are the patch keys a predefined profile accepts. The
// remaining custom-profile keys are rejected with an actionable error rather
// than silently dropped.
var dlpPredefinedPatchable = map[string]string{
	"ai_context_enabled":   "--ai-context-enabled",
	"allowed_match_count":  "--allowed-match-count",
	"confidence_threshold": "--confidence-threshold",
	"ocr_enabled":          "--ocr-enabled",
}

func newDLPCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dlp",
		Short: "Manage Data Loss Prevention profiles, datasets, and payload logging",
		Long: `Manage Cloudflare Data Loss Prevention.

Covers the common workflows: browsing profiles, custom profile CRUD, tuning
predefined profiles, dataset management (Exact Data Match and custom word
lists), and payload log settings.

Entries, email scanner rules, data classes, data tags, sensitivity groups,
document fingerprints, custom prompt topics, and account DLP settings are
available through the generated layer (cf api dlp --help).`,
	}
	cmd.AddCommand(
		newDLPProfileCmd(g),
		newDLPDatasetCmd(g),
		newDLPPayloadLogCmd(g),
	)
	return cmd
}

// --- shared helpers --------------------------------------------------------

// dlpAccountID validates the resolved account scope. Every DLP endpoint is
// account-scoped.
func dlpAccountID(configured string) (string, error) {
	if configured == "" {
		return "", errors.New("no account specified: pass --account-id, set CLOUDFLARE_ACCOUNT_ID, or configure a profile")
	}
	return configured, nil
}

// dlpPath builds an account-scoped DLP path; suffix starts with "/".
func dlpPath(accountID, suffix string) string {
	return "/accounts/" + url.PathEscape(accountID) + "/dlp" + suffix
}

// dlpUUID matches the 8-4-4-4-12 hex form the spec documents (format: uuid)
// for every DLP profile, dataset, and entry identifier. Case is not
// significant.
var dlpUUID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// dlpRequireID rejects empty or malformed identifiers before any client
// construction or network work.
func dlpRequireID(kind, id string) (string, error) {
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return "", fmt.Errorf("%s must not be empty", kind)
	}
	if !dlpUUID.MatchString(trimmed) {
		return "", fmt.Errorf("invalid %s %q: expected a UUID like 384e129d-25bd-403c-8019-bc19eb7a8a5f", kind, trimmed)
	}
	return trimmed, nil
}

// dlpIsBase64 reports whether s decodes as base64 in any of the encodings a
// public key is plausibly pasted in (standard or URL alphabet, padded or not).
func dlpIsBase64(s string) bool {
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding,
		base64.URLEncoding, base64.RawURLEncoding,
	} {
		if _, err := enc.DecodeString(s); err == nil {
			return true
		}
	}
	return false
}

func runDLPRequest(cmd *cobra.Command, g *globalOpts, client *api.Client, req api.Request) error {
	if g.DryRun {
		dump, err := client.Dump(req)
		if err != nil {
			return err
		}
		return g.renderValue(cmd, dump, output.JSON)
	}
	env, err := client.Do(cmd.Context(), req)
	if err != nil {
		return err
	}
	return g.renderResult(cmd, env.Result, output.JSON)
}

// runDLPList sends a list request and renders it as a table, falling back to
// JSON when the decoder cannot read the result. filter, when non-nil, is
// applied to the result array before rendering so --output json and --query
// see the same rows as the table.
func runDLPList(cmd *cobra.Command, g *globalOpts, client *api.Client, req api.Request,
	filter func(json.RawMessage) (json.RawMessage, error),
	table func(json.RawMessage) ([]string, [][]string, bool)) error {
	if g.DryRun {
		dump, err := client.Dump(req)
		if err != nil {
			return err
		}
		return g.renderValue(cmd, dump, output.JSON)
	}
	env, err := client.DoAutoPaginate(cmd.Context(), req)
	if err != nil {
		return err
	}
	result := env.Result
	if filter != nil {
		filtered, err := filter(result)
		if err != nil {
			return err
		}
		result = filtered
	}
	format := g.format(output.Table)
	if g.Query != "" || format != output.Table {
		return g.renderResult(cmd, result, output.JSON)
	}
	headers, rows, ok := table(result)
	if !ok {
		return output.RenderRaw(cmd.OutOrStdout(), output.JSON, result)
	}
	return output.RenderTable(cmd.OutOrStdout(), headers, rows)
}

// dlpValidateAllowedMatchCount enforces the 0-1000 bound the create and
// predefined-update schemas document.
func dlpValidateAllowedMatchCount(n int) error {
	if n < 0 || n > dlpMaxAllowedMatchCount {
		return fmt.Errorf("--allowed-match-count must be between 0 and %d", dlpMaxAllowedMatchCount)
	}
	return nil
}

// dlpValidateInt32 holds a flag to the int32 representation the schema
// documents (format: int32). Fields whose schema declares no minimum or
// maximum still cannot carry a value the wire type cannot hold.
func dlpValidateInt32(flag string, n int) error {
	if n < math.MinInt32 || n > math.MaxInt32 {
		return fmt.Errorf("%s must fit in a 32-bit integer (%d to %d)", flag, math.MinInt32, math.MaxInt32)
	}
	return nil
}

// dlpValidateUnsignedInt32 holds a flag to a schema documenting format: int32
// with minimum 0.
func dlpValidateUnsignedInt32(flag string, n int) error {
	if n < 0 || n > math.MaxInt32 {
		return fmt.Errorf("%s must be between 0 and %d", flag, math.MaxInt32)
	}
	return nil
}

// dlpOneOf reports whether v is in allowed, and builds the error listing the
// accepted values when it is not.
func dlpOneOf(flag, v string, allowed []string) error {
	for _, a := range allowed {
		if v == a {
			return nil
		}
	}
	return fmt.Errorf("invalid %s %q: expected one of %s", flag, v, strings.Join(allowed, ", "))
}

// dlpNumber coerces a JSON number (float64 from a decoded response) or a
// native int into an int64.
func dlpNumber(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int64:
		return n, true
	case float64:
		if n != math.Trunc(n) {
			return 0, false
		}
		return int64(n), true
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			return 0, false
		}
		return i, true
	}
	return 0, false
}

// --- profiles --------------------------------------------------------------

func newDLPProfileCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "profile",
		Short: "Manage DLP profiles",
	}
	cmd.AddCommand(
		newDLPProfileListCmd(g),
		newDLPProfileGetCmd(g),
		newDLPProfileCreateCmd(g),
		newDLPProfileUpdateCmd(g),
		newDLPProfileDeleteCmd(g),
	)
	return cmd
}

func dlpProfilesPath(accountID string) string { return dlpPath(accountID, "/profiles") }

func dlpProfilePath(accountID, profileID string) string {
	return dlpPath(accountID, "/profiles/"+url.PathEscape(profileID))
}

func dlpCustomProfilePath(accountID, profileID string) string {
	return dlpPath(accountID, "/profiles/custom/"+url.PathEscape(profileID))
}

func dlpPredefinedProfilePath(accountID, profileID string) string {
	return dlpPath(accountID, "/profiles/predefined/"+url.PathEscape(profileID))
}

// dlpProfileRow is the table projection of a profile. Predefined profiles
// carry no description or timestamps, so the columns stay to the fields every
// profile type has.
type dlpProfileRow struct {
	ID                  string            `json:"id"`
	Type                string            `json:"type"`
	Name                string            `json:"name"`
	OCREnabled          bool              `json:"ocr_enabled"`
	AllowedMatchCount   *int64            `json:"allowed_match_count"`
	ConfidenceThreshold string            `json:"confidence_threshold"`
	Entries             []json.RawMessage `json:"entries"`
	SharedEntries       []json.RawMessage `json:"shared_entries"`
}

func dlpProfileTable(raw json.RawMessage) ([]string, [][]string, bool) {
	var profiles []dlpProfileRow
	if err := json.Unmarshal(raw, &profiles); err != nil {
		return nil, nil, false
	}
	rows := make([][]string, 0, len(profiles))
	for _, p := range profiles {
		matches := ""
		if p.AllowedMatchCount != nil {
			matches = strconv.FormatInt(*p.AllowedMatchCount, 10)
		}
		rows = append(rows, []string{
			p.ID,
			p.Type,
			output.Cell(p.Name),
			strconv.Itoa(len(p.Entries) + len(p.SharedEntries)),
			matches,
			p.ConfidenceThreshold,
			strconv.FormatBool(p.OCREnabled),
		})
	}
	return []string{"ID", "TYPE", "NAME", "ENTRIES", "ALLOWED MATCHES", "CONFIDENCE", "OCR"}, rows, true
}

// dlpFilterProfilesByType keeps only profiles whose discriminator matches
// wanted. The API has no type filter, so this happens client-side.
func dlpFilterProfilesByType(raw json.RawMessage, wanted string) (json.RawMessage, error) {
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("--type filter: the profiles response was not a list")
	}
	kept := make([]json.RawMessage, 0, len(items))
	for _, item := range items {
		var probe struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(item, &probe); err != nil {
			continue
		}
		if probe.Type == wanted {
			kept = append(kept, item)
		}
	}
	return json.Marshal(kept)
}

func newDLPProfileListCmd(g *globalOpts) *cobra.Command {
	var profileType string
	var all bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List DLP profiles",
		Long: `List DLP profiles (custom, predefined, and integration).

--type filters the response after it is fetched: the API returns every profile
in one call and has no type parameter.

Examples:

  cf dlp profile list
  cf dlp profile list --type custom
  cf dlp profile list --all --output json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().Changed("type") {
				if err := dlpOneOf("--type", profileType, dlpProfileTypes); err != nil {
					return err
				}
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := dlpAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			q := url.Values{}
			if cmd.Flags().Changed("all") {
				q.Set("all", strconv.FormatBool(all))
			}
			req := api.Request{Method: "GET", Path: dlpProfilesPath(accountID), Query: q}
			var filter func(json.RawMessage) (json.RawMessage, error)
			if cmd.Flags().Changed("type") {
				filter = func(raw json.RawMessage) (json.RawMessage, error) {
					return dlpFilterProfilesByType(raw, profileType)
				}
			}
			return runDLPList(cmd, g, client, req, filter, dlpProfileTable)
		},
	}
	cmd.Flags().StringVar(&profileType, "type", "", "keep only profiles of this type (custom, predefined, integration)")
	cmd.Flags().BoolVar(&all, "all", false, "include profiles this account cannot access")
	return cmd
}

func newDLPProfileGetCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <profile-id>",
		Short: "Show one DLP profile",
		Long: `Show a DLP profile of any type, including its entries.

Examples:

  cf dlp profile get 384e129d-25bd-403c-8019-bc19eb7a8a5f`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			profileID, err := dlpRequireID("profile ID", args[0])
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := dlpAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: dlpProfilePath(accountID, profileID)}
			return runDLPRequest(cmd, g, client, req)
		},
	}
	return cmd
}

// dlpProfileFlags carries the profile fields this porcelain writes.
type dlpProfileFlags struct {
	name                string
	description         string
	allowedMatchCount   int
	confidenceThreshold string
	ocrEnabled          bool
	aiContextEnabled    bool
	entries             []string
	sharedEntries       []string
}

func bindDLPProfileFlags(cmd *cobra.Command, f *dlpProfileFlags, create bool) {
	cmd.Flags().StringVar(&f.name, "name", "", "profile name")
	cmd.Flags().StringVar(&f.description, "description", "", "profile description")
	cmd.Flags().IntVar(&f.allowedMatchCount, "allowed-match-count", 0,
		fmt.Sprintf("policies trigger above this many matches (0-%d)", dlpMaxAllowedMatchCount))
	cmd.Flags().StringVar(&f.confidenceThreshold, "confidence-threshold", "",
		"minimum detection confidence (documented values: "+strings.Join(dlpConfidenceThresholds, ", ")+")")
	cmd.Flags().BoolVar(&f.ocrEnabled, "ocr-enabled", false, "scan text inside images with OCR")
	cmd.Flags().BoolVar(&f.aiContextEnabled, "ai-context-enabled", false, "use AI context analysis for supported entries")
	entryHelp := "detection entry as JSON, repeatable (replaces every custom entry on update)"
	if create {
		entryHelp = "detection entry as JSON, repeatable"
	}
	cmd.Flags().StringArrayVar(&f.entries, "entry", nil, entryHelp)
	cmd.Flags().StringArrayVar(&f.sharedEntries, "shared-entry", nil, "entry ID from another profile to include, repeatable")
}

// dlpProfilePatch turns the flags into the patch fragment for a profile write.
// It validates every value locally, before any client or network work.
func dlpProfilePatch(cmd *cobra.Command, f dlpProfileFlags, create bool) (map[string]any, error) {
	patch := map[string]any{}
	if cmd.Flags().Changed("name") {
		if strings.TrimSpace(f.name) == "" {
			return nil, errors.New("--name must not be empty")
		}
		patch["name"] = f.name
	}
	if cmd.Flags().Changed("description") {
		patch["description"] = f.description
	}
	if cmd.Flags().Changed("allowed-match-count") {
		// Only the create schema bounds this field to 0-1000; the custom
		// update schema declares no minimum or maximum, so it is held to the
		// int32 representation alone. The predefined bound is applied once
		// the profile's type is known.
		if create {
			if err := dlpValidateAllowedMatchCount(f.allowedMatchCount); err != nil {
				return nil, err
			}
		} else if err := dlpValidateInt32("--allowed-match-count", f.allowedMatchCount); err != nil {
			return nil, err
		}
		patch["allowed_match_count"] = f.allowedMatchCount
	}
	if cmd.Flags().Changed("confidence-threshold") {
		patch["confidence_threshold"] = f.confidenceThreshold
	}
	if cmd.Flags().Changed("ocr-enabled") {
		patch["ocr_enabled"] = f.ocrEnabled
	}
	if cmd.Flags().Changed("ai-context-enabled") {
		patch["ai_context_enabled"] = f.aiContextEnabled
	}
	if len(f.entries) > 0 {
		entries := make([]any, 0, len(f.entries))
		for i, raw := range f.entries {
			entry, err := dlpParseEntry(raw, create)
			if err != nil {
				return nil, fmt.Errorf("--entry #%d: %w", i+1, err)
			}
			entries = append(entries, entry)
		}
		patch["entries"] = entries
	}
	if len(f.sharedEntries) > 0 {
		shared := make([]any, 0, len(f.sharedEntries))
		seen := map[string]bool{}
		for i, raw := range f.sharedEntries {
			id, err := dlpRequireID("shared entry ID", raw)
			if err != nil {
				return nil, fmt.Errorf("--shared-entry #%d: %w", i+1, err)
			}
			if seen[id] {
				return nil, fmt.Errorf("--shared-entry %s was given twice", id)
			}
			seen[id] = true
			shared = append(shared, map[string]any{"entry_id": id, "enabled": true})
		}
		patch["shared_entries"] = shared
	}
	return patch, nil
}

// dlpParseEntry decodes one --entry value. On create the API accepts a custom
// pattern entry or a word list (dlp_EntryOfNewProfile), neither of which has
// an entry_id; on update it accepts only custom pattern entries, optionally
// carrying entry_id to keep an existing entry (dlp_ProfileEntryUpdate).
// "enabled" is required by the API and defaults to true here.
func dlpParseEntry(raw string, create bool) (map[string]any, error) {
	var decoded any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	obj, ok := decoded.(map[string]any)
	if !ok {
		return nil, errors.New(`must be a JSON object, e.g. {"name":"Employee ID","pattern":{"regex":"EMP-[0-9]{6}"}}`)
	}
	// "words" is accepted by the key check in both modes so an update can say
	// why word lists are rejected instead of calling the field unknown.
	allowed := map[string]bool{
		"name": true, "enabled": true, "description": true,
		"pattern": true, "entry_id": true, "words": true,
	}
	for k := range obj {
		if !allowed[k] {
			keys := make([]string, 0, len(allowed))
			for a := range allowed {
				keys = append(keys, a)
			}
			sort.Strings(keys)
			return nil, fmt.Errorf("unknown field %q (accepted: %s)", k, strings.Join(keys, ", "))
		}
	}
	entry := map[string]any{}
	name, ok := obj["name"].(string)
	if !ok || strings.TrimSpace(name) == "" {
		return nil, errors.New(`"name" is required and must be a non-empty string`)
	}
	entry["name"] = name
	if v, ok := obj["enabled"]; ok {
		b, ok := v.(bool)
		if !ok {
			return nil, errors.New(`"enabled" must be true or false`)
		}
		entry["enabled"] = b
	} else {
		entry["enabled"] = true
	}
	_, hasDescription := obj["description"]
	_, hasPattern := obj["pattern"]
	_, hasWords := obj["words"]
	if hasDescription && hasWords {
		return nil, errors.New(`word list entries take no "description"`)
	}
	if hasDescription {
		v := obj["description"]
		if v == nil {
			entry["description"] = nil
		} else if s, ok := v.(string); ok {
			entry["description"] = s
		} else {
			return nil, errors.New(`"description" must be a string or null`)
		}
	}
	if v, ok := obj["entry_id"]; ok {
		if create {
			return nil, errors.New(`"entry_id" is only accepted when updating a profile; a created entry gets its ID from the API`)
		}
		id, ok := v.(string)
		if !ok {
			return nil, errors.New(`"entry_id" must be a string`)
		}
		checked, err := dlpRequireID("entry_id", id)
		if err != nil {
			return nil, err
		}
		entry["entry_id"] = checked
	}
	switch {
	case hasPattern && hasWords:
		return nil, errors.New(`use either "pattern" or "words", not both`)
	case hasPattern:
		pattern, err := dlpParseEntryPattern(obj["pattern"])
		if err != nil {
			return nil, err
		}
		entry["pattern"] = pattern
	case hasWords:
		if !create {
			return nil, errors.New(`has "words"; word list entries can only be created, not sent in a profile update`)
		}
		words, err := dlpParseEntryWords(obj["words"])
		if err != nil {
			return nil, err
		}
		entry["words"] = words
	case create:
		return nil, errors.New(`needs either "pattern" (regex entry) or "words" (word list entry)`)
	default:
		return nil, errors.New(`needs a "pattern"; word list entries can only be created, not sent in a profile update`)
	}
	return entry, nil
}

func dlpParseEntryPattern(v any) (map[string]any, error) {
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New(`"pattern" must be an object, e.g. {"regex":"EMP-[0-9]{6}"}`)
	}
	for k := range obj {
		if k != "regex" && k != "validation" {
			return nil, fmt.Errorf(`unknown field %q in "pattern" (accepted: regex, validation)`, k)
		}
	}
	regex, ok := obj["regex"].(string)
	if !ok || regex == "" {
		return nil, errors.New(`"pattern.regex" is required and must be a non-empty string`)
	}
	pattern := map[string]any{"regex": regex}
	if v, ok := obj["validation"]; ok {
		s, ok := v.(string)
		if !ok {
			return nil, errors.New(`"pattern.validation" must be a string`)
		}
		if err := dlpOneOf(`"pattern.validation"`, s, []string{"luhn"}); err != nil {
			return nil, err
		}
		pattern["validation"] = s
	}
	return pattern, nil
}

func dlpParseEntryWords(v any) ([]any, error) {
	list, ok := v.([]any)
	if !ok {
		return nil, errors.New(`"words" must be an array of strings`)
	}
	if len(list) == 0 {
		return nil, errors.New(`"words" must contain at least one word`)
	}
	words := make([]any, 0, len(list))
	for i, item := range list {
		s, ok := item.(string)
		if !ok || s == "" {
			return nil, fmt.Errorf(`"words"[%d] must be a non-empty string`, i)
		}
		words = append(words, s)
	}
	return words, nil
}

func newDLPProfileCreateCmd(g *globalOpts) *cobra.Command {
	var f dlpProfileFlags
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a custom DLP profile",
		Long: `Create a custom DLP profile.

Each --entry is a JSON object: a regex entry {"name":..., "pattern":{"regex":...}}
or a word list {"name":..., "words":[...]}. "enabled" defaults to true.
--shared-entry adds an existing entry from another profile (for example a
predefined detector) to this profile.

Examples:

  cf dlp profile create --name "Employee IDs" \
    --entry '{"name":"Employee ID","pattern":{"regex":"EMP-[0-9]{6}"}}'
  cf dlp profile create --name "Project codenames" --confidence-threshold high \
    --entry '{"name":"Codenames","words":["bluejay","redwood"]}' --ocr-enabled`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			patch, err := dlpProfilePatch(cmd, f, true)
			if err != nil {
				return err
			}
			if _, ok := patch["name"]; !ok {
				return errors.New("--name is required")
			}
			body, err := json.Marshal(patch)
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := dlpAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			req := api.Request{Method: "POST", Path: dlpPath(accountID, "/profiles/custom"), Body: body}
			return runDLPRequest(cmd, g, client, req)
		},
	}
	bindDLPProfileFlags(cmd, &f, true)
	_ = cmd.MarkFlagRequired("name")
	return cmd
}

func newDLPProfileUpdateCmd(g *globalOpts) *cobra.Command {
	var f dlpProfileFlags
	cmd := &cobra.Command{
		Use:   "update <profile-id>",
		Short: "Update a custom or predefined DLP profile",
		Long: `Update fields of a DLP profile.

This command reads the profile first, to learn whether it is custom or
predefined and to write it to the matching endpoint. --dry-run performs that
read but never sends the write.

A custom profile write requires a complete object, so your flags are merged
onto the profile as the API returned it (preserving fields this CLI does not
model) with read-only properties stripped. A predefined profile write requires
nothing, so only the fields you pass are sent.

Predefined profiles accept only --allowed-match-count, --confidence-threshold,
--ocr-enabled, and --ai-context-enabled; their name, description, and entries
are owned by Cloudflare.

Custom entries are left untouched unless --entry is given, in which case the
listed entries replace every custom entry the profile owns. Include
"entry_id" in an entry to keep an existing one. Individual entries can also be
managed with cf api dlp entries-create.

Examples:

  cf dlp profile update 384e129d-25bd-403c-8019-bc19eb7a8a5f --allowed-match-count 5
  cf dlp profile update 384e129d-25bd-403c-8019-bc19eb7a8a5f --name "Employee IDs (EU)"
  cf dlp profile update 384e129d-25bd-403c-8019-bc19eb7a8a5f \
    --entry '{"entry_id":"9c4b1e6c-3f9d-4a2c-9d4e-9a1b2c3d4e5f","name":"Employee ID","pattern":{"regex":"EMP-[0-9]{6}"}}'`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			profileID, err := dlpRequireID("profile ID", args[0])
			if err != nil {
				return err
			}
			patch, err := dlpProfilePatch(cmd, f, false)
			if err != nil {
				return err
			}
			if len(patch) == 0 {
				return errors.New("nothing to update: pass at least one of --name, --description, --allowed-match-count, --confidence-threshold, --ocr-enabled, --ai-context-enabled, --entry, --shared-entry")
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := dlpAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			current, err := dlpFetchProfile(cmd.Context(), client, accountID, profileID)
			if err != nil {
				return err
			}
			profileType, _ := current["type"].(string)
			var req api.Request
			switch profileType {
			case "custom":
				body, err := dlpCustomProfileUpdateBody(current, patch)
				if err != nil {
					return fmt.Errorf("custom profile %s cannot be updated: %w", profileID, err)
				}
				req = api.Request{Method: "PUT", Path: dlpCustomProfilePath(accountID, profileID), Body: body}
			case "predefined":
				body, err := dlpPredefinedProfileUpdateBody(patch)
				if err != nil {
					return fmt.Errorf("predefined profile %s cannot be updated: %w", profileID, err)
				}
				req = api.Request{Method: "PUT", Path: dlpPredefinedProfilePath(accountID, profileID), Body: body}
			case "integration":
				return fmt.Errorf("profile %s is an integration profile; it is managed by its integration and cannot be updated with cf dlp", profileID)
			default:
				return fmt.Errorf("profile %s has unknown type %q; update it with cf api dlp", profileID, profileType)
			}
			return runDLPRequest(cmd, g, client, req)
		},
	}
	bindDLPProfileFlags(cmd, &f, false)
	return cmd
}

// dlpFetchProfile reads a profile as a raw object so unmodeled fields survive
// the read-merge-write cycle.
func dlpFetchProfile(ctx context.Context, client *api.Client, accountID, profileID string) (map[string]any, error) {
	env, err := client.Do(ctx, api.Request{Method: "GET", Path: dlpProfilePath(accountID, profileID)})
	if err != nil {
		return nil, fmt.Errorf("read profile %s before update: %w", profileID, err)
	}
	var obj map[string]any
	if err := json.Unmarshal(env.Result, &obj); err != nil || obj == nil {
		return nil, fmt.Errorf("read profile %s before update: unexpected response", profileID)
	}
	return obj, nil
}

// dlpCustomProfileUpdateBody merges the flag patch onto the profile as the API
// returned it. Read-only fields are dropped, shared entries are projected back
// into their write shape, and "entries" is omitted unless the caller asked to
// replace it — the API documents omitted entries as "not changed".
func dlpCustomProfileUpdateBody(current, patch map[string]any) ([]byte, error) {
	out := make(map[string]any, len(current)+len(patch))
	for k, v := range current {
		out[k] = v
	}
	for _, k := range dlpProfileReadOnly {
		delete(out, k)
	}
	delete(out, "entries")
	if v, ok := out["shared_entries"]; ok {
		shared, err := dlpProjectSharedEntries(v)
		if err != nil {
			return nil, err
		}
		if shared == nil {
			delete(out, "shared_entries")
		} else {
			out["shared_entries"] = shared
		}
	}
	for k, v := range patch {
		out[k] = v
	}
	if err := dlpValidateCustomProfileBody(out); err != nil {
		return nil, err
	}
	return json.Marshal(out)
}

// dlpPredefinedProfileUpdateBody builds the predefined write body. That schema
// (dlp_PredefinedProfileUpdate) requires nothing, so the body carries only the
// fields the caller changed: nothing is echoed back from the read, and the
// deprecated context_awareness and entries fields are never sent.
func dlpPredefinedProfileUpdateBody(patch map[string]any) ([]byte, error) {
	for k := range patch {
		if _, ok := dlpPredefinedPatchable[k]; !ok {
			flags := make([]string, 0, len(dlpPredefinedPatchable))
			for _, flag := range dlpPredefinedPatchable {
				flags = append(flags, flag)
			}
			sort.Strings(flags)
			return nil, fmt.Errorf("predefined profiles accept only %s", strings.Join(flags, ", "))
		}
	}
	if v, ok := patch["allowed_match_count"]; ok {
		n, ok := dlpNumber(v)
		if !ok {
			return nil, errors.New("--allowed-match-count must be a whole number")
		}
		if err := dlpValidateAllowedMatchCount(int(n)); err != nil {
			return nil, err
		}
	}
	return json.Marshal(patch)
}

// dlpProjectSharedEntries maps the entry objects a profile GET returns onto
// the {entry_id, enabled} pairs the write schema takes, so an unrelated field
// update cannot silently drop the profile's shared entries.
func dlpProjectSharedEntries(v any) ([]any, error) {
	if v == nil {
		return nil, nil
	}
	items, ok := v.([]any)
	if !ok {
		return nil, errors.New("the API returned shared_entries in an unexpected shape")
	}
	out := make([]any, 0, len(items))
	for i, item := range items {
		obj, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("shared_entries[%d] is not an object", i)
		}
		id, ok := obj["id"].(string)
		if !ok || id == "" {
			return nil, fmt.Errorf("shared_entries[%d] has no id; update this profile with cf api dlp profiles-custom-replace", i)
		}
		// These IDs are re-sent in the complete write body, so they are held
		// to the same documented UUID form as an ID typed on the command line.
		checked, err := dlpRequireID(fmt.Sprintf("shared_entries[%d] id", i), id)
		if err != nil {
			return nil, fmt.Errorf("%w; update this profile with cf api dlp profiles-custom-replace", err)
		}
		enabled, ok := obj["enabled"].(bool)
		if !ok {
			return nil, fmt.Errorf("shared_entries[%d] has no enabled flag; update this profile with cf api dlp profiles-custom-replace", i)
		}
		out = append(out, map[string]any{"entry_id": checked, "enabled": enabled})
	}
	return out, nil
}

// dlpValidateCustomProfileBody checks the one thing the custom write schema
// requires of a complete body: a name. Values carried over unchanged from the
// read are not re-validated, so a field the API grows later cannot make an
// unrelated update fail here.
func dlpValidateCustomProfileBody(body map[string]any) error {
	name, ok := body["name"].(string)
	if !ok || strings.TrimSpace(name) == "" {
		return errors.New("the API requires a name and the current profile has none; pass --name")
	}
	return nil
}

func newDLPProfileDeleteCmd(g *globalOpts) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <profile-id>",
		Short: "Delete a custom DLP profile",
		Long: `Delete a custom DLP profile.

Only custom profiles can be deleted; predefined profiles belong to Cloudflare.

Examples:

  cf dlp profile delete 384e129d-25bd-403c-8019-bc19eb7a8a5f --force`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			profileID, err := dlpRequireID("profile ID", args[0])
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := dlpAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			if !force && !g.DryRun {
				if !confirm(fmt.Sprintf("Delete custom DLP profile %s and its entries from account %s? Gateway policies referencing it will stop matching.", profileID, accountID)) {
					return errors.New("aborted (pass --force to skip confirmation)")
				}
			}
			req := api.Request{Method: "DELETE", Path: dlpCustomProfilePath(accountID, profileID)}
			return runDLPRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	return cmd
}

// --- datasets --------------------------------------------------------------

func newDLPDatasetCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dataset",
		Short: "Manage DLP datasets (Exact Data Match and custom word lists)",
	}
	cmd.AddCommand(
		newDLPDatasetListCmd(g),
		newDLPDatasetGetCmd(g),
		newDLPDatasetCreateCmd(g),
		newDLPDatasetUpdateCmd(g),
		newDLPDatasetUploadCmd(g),
		newDLPDatasetDeleteCmd(g),
	)
	return cmd
}

func dlpDatasetsPath(accountID string) string { return dlpPath(accountID, "/datasets") }

func dlpDatasetPath(accountID, datasetID string) string {
	return dlpPath(accountID, "/datasets/"+url.PathEscape(datasetID))
}

type dlpDatasetRow struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Status    string            `json:"status"`
	Secret    bool              `json:"secret"`
	NumCells  int64             `json:"num_cells"`
	Columns   []json.RawMessage `json:"columns"`
	Uploads   []json.RawMessage `json:"uploads"`
	UpdatedAt string            `json:"updated_at"`
}

func dlpDatasetTable(raw json.RawMessage) ([]string, [][]string, bool) {
	var datasets []dlpDatasetRow
	if err := json.Unmarshal(raw, &datasets); err != nil {
		return nil, nil, false
	}
	rows := make([][]string, 0, len(datasets))
	for _, d := range datasets {
		rows = append(rows, []string{
			d.ID,
			output.Cell(d.Name),
			d.Status,
			strconv.FormatBool(d.Secret),
			strconv.FormatInt(d.NumCells, 10),
			strconv.Itoa(len(d.Columns)),
			strconv.Itoa(len(d.Uploads)),
			d.UpdatedAt,
		})
	}
	return []string{"ID", "NAME", "STATUS", "SECRET", "CELLS", "COLUMNS", "VERSIONS", "UPDATED"}, rows, true
}

func newDLPDatasetListCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List DLP datasets",
		Long: `List DLP datasets with their upload status.

Examples:

  cf dlp dataset list
  cf dlp dataset list --output json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := dlpAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: dlpDatasetsPath(accountID)}
			return runDLPList(cmd, g, client, req, nil, dlpDatasetTable)
		},
	}
	return cmd
}

func newDLPDatasetGetCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <dataset-id>",
		Short: "Show one DLP dataset",
		Long: `Show a DLP dataset, including its columns and upload versions.

Examples:

  cf dlp dataset get 182bd5e5-6e1a-4fe4-a799-aa6d9a6ab26e`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			datasetID, err := dlpRequireID("dataset ID", args[0])
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := dlpAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: dlpDatasetPath(accountID, datasetID)}
			return runDLPRequest(cmd, g, client, req)
		},
	}
	return cmd
}

func newDLPDatasetCreateCmd(g *globalOpts) *cobra.Command {
	var name, description string
	var secret, caseSensitive bool
	var encodingVersion int
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a DLP dataset",
		Long: `Create a DLP dataset, then upload its contents with cf dlp dataset upload.

A secret dataset (--secret) returns a secret for the Exact Data Match encoder;
its contents are uploaded already encoded. A non-secret dataset is a custom
word list uploaded in plaintext, and only those can be case-insensitive.

Examples:

  cf dlp dataset create --name "Customer emails" --secret
  cf dlp dataset create --name "Project codenames" --secret=false --case-sensitive=false`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := dlpDatasetCreateBody(cmd, name, description, secret, caseSensitive, encodingVersion)
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := dlpAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			req := api.Request{Method: "POST", Path: dlpDatasetsPath(accountID), Body: body}
			return runDLPRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "dataset name")
	cmd.Flags().StringVar(&description, "description", "", "dataset description")
	cmd.Flags().BoolVar(&secret, "secret", false, "generate a secret Exact Data Match dataset")
	cmd.Flags().BoolVar(&caseSensitive, "case-sensitive", false, "match words case-sensitively (word lists only)")
	cmd.Flags().IntVar(&encodingVersion, "encoding-version", 0, "dataset encoding version (0 or 1 for single-column, 2 for multi-column CSV)")
	_ = cmd.MarkFlagRequired("name")
	return cmd
}

// dlpDatasetCreateBody builds and validates the create body, including the
// documented rule that case_sensitive cannot be false unless the dataset is
// explicitly non-secret.
func dlpDatasetCreateBody(cmd *cobra.Command, name, description string, secret, caseSensitive bool, encodingVersion int) ([]byte, error) {
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("--name is required")
	}
	body := map[string]any{"name": name}
	if cmd.Flags().Changed("description") {
		body["description"] = description
	}
	if cmd.Flags().Changed("secret") {
		body["secret"] = secret
	}
	if cmd.Flags().Changed("case-sensitive") {
		if !caseSensitive && (!cmd.Flags().Changed("secret") || secret) {
			return nil, errors.New("--case-sensitive=false requires --secret=false: only non-secret custom word lists can match case-insensitively")
		}
		body["case_sensitive"] = caseSensitive
	}
	if cmd.Flags().Changed("encoding-version") {
		if err := dlpValidateUnsignedInt32("--encoding-version", encodingVersion); err != nil {
			return nil, err
		}
		body["encoding_version"] = encodingVersion
	}
	return json.Marshal(body)
}

func newDLPDatasetUpdateCmd(g *globalOpts) *cobra.Command {
	var name, description string
	var caseSensitive bool
	cmd := &cobra.Command{
		Use:   "update <dataset-id>",
		Short: "Update a DLP dataset's details",
		Long: `Update a dataset's name, description, or case sensitivity.

Only the fields you pass are sent; the dataset's uploaded contents are not
affected. Use cf dlp dataset upload to replace the contents.

Examples:

  cf dlp dataset update 182bd5e5-6e1a-4fe4-a799-aa6d9a6ab26e --description "Q3 customer list"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			datasetID, err := dlpRequireID("dataset ID", args[0])
			if err != nil {
				return err
			}
			patch := map[string]any{}
			if cmd.Flags().Changed("name") {
				if strings.TrimSpace(name) == "" {
					return errors.New("--name must not be empty")
				}
				patch["name"] = name
			}
			if cmd.Flags().Changed("description") {
				patch["description"] = description
			}
			if cmd.Flags().Changed("case-sensitive") {
				patch["case_sensitive"] = caseSensitive
			}
			if len(patch) == 0 {
				return errors.New("nothing to update: pass at least one of --name, --description, --case-sensitive")
			}
			body, err := json.Marshal(patch)
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := dlpAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			req := api.Request{Method: "PUT", Path: dlpDatasetPath(accountID, datasetID), Body: body}
			return runDLPRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "new dataset name (must be unique)")
	cmd.Flags().StringVar(&description, "description", "", "new dataset description")
	cmd.Flags().BoolVar(&caseSensitive, "case-sensitive", false, "match words case-sensitively (word lists only)")
	return cmd
}

func newDLPDatasetUploadCmd(g *globalOpts) *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "upload <dataset-id>",
		Short: "Upload a new version of a DLP dataset",
		Long: `Upload a new version of a dataset's contents.

This is two requests: one to reserve a version, then the file body itself. A
custom word list is uploaded as plaintext (one entry per line); a secret
Exact Data Match dataset must already be encoded with the EDM encoder using
the secret returned when the dataset was created.

--dry-run shows the version request only, because the version to upload to
does not exist until that request is sent.

Examples:

  cf dlp dataset upload 182bd5e5-6e1a-4fe4-a799-aa6d9a6ab26e --file codenames.txt`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			datasetID, err := dlpRequireID("dataset ID", args[0])
			if err != nil {
				return err
			}
			if strings.TrimSpace(file) == "" {
				return errors.New("--file is required")
			}
			info, err := os.Stat(file)
			if err != nil {
				return fmt.Errorf("read --file %s: %w", file, err)
			}
			if info.IsDir() {
				return fmt.Errorf("read --file %s: is a directory, expected a dataset file", file)
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := dlpAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			prepare := api.Request{Method: "POST", Path: dlpDatasetPath(accountID, datasetID) + "/upload"}
			if g.DryRun {
				fmt.Fprintf(cmd.ErrOrStderr(),
					"dry run: showing the version request only; the upload would then POST %d byte(s) from %s to %s/upload/<version> as application/octet-stream\n",
					info.Size(), file, dlpDatasetPath(accountID, datasetID))
				dump, err := client.Dump(prepare)
				if err != nil {
					return err
				}
				return g.renderValue(cmd, dump, output.JSON)
			}
			env, err := client.Do(cmd.Context(), prepare)
			if err != nil {
				return err
			}
			var version struct {
				Version *int64 `json:"version"`
			}
			if err := json.Unmarshal(env.Result, &version); err != nil || version.Version == nil {
				return fmt.Errorf("dataset %s: the API did not return a version to upload to", datasetID)
			}
			f, err := os.Open(file)
			if err != nil {
				return fmt.Errorf("read --file %s: %w", file, err)
			}
			defer f.Close()
			fmt.Fprintf(cmd.ErrOrStderr(), "uploading %d byte(s) from %s as version %d\n", info.Size(), file, *version.Version)
			upload := api.Request{
				Method:      "POST",
				Path:        dlpDatasetPath(accountID, datasetID) + "/upload/" + strconv.FormatInt(*version.Version, 10),
				ContentType: "application/octet-stream",
			}
			resp, err := client.DoStream(cmd.Context(), upload, f)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			return dlpRenderStreamedEnvelope(cmd, g, resp.Body)
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "path to the dataset contents to upload")
	_ = cmd.MarkFlagRequired("file")
	return cmd
}

// dlpRenderStreamedEnvelope parses a streamed success response as the standard
// envelope and renders its result.
func dlpRenderStreamedEnvelope(cmd *cobra.Command, g *globalOpts, body io.Reader) error {
	data, err := io.ReadAll(io.LimitReader(body, 10<<20))
	if err != nil {
		return err
	}
	var env api.Envelope
	if err := json.Unmarshal(bytes.TrimSpace(data), &env); err != nil {
		return g.renderValue(cmd, string(data), output.JSON)
	}
	if !env.Success {
		return fmt.Errorf("dataset upload failed: %s", dlpErrorText(env.Errors))
	}
	return g.renderResult(cmd, env.Result, output.JSON)
}

func dlpErrorText(msgs []api.Message) string {
	if len(msgs) == 0 {
		return "the API reported success=false without an error message"
	}
	parts := make([]string, 0, len(msgs))
	for _, m := range msgs {
		parts = append(parts, fmt.Sprintf("%d: %s", m.Code, m.Message))
	}
	return strings.Join(parts, "; ")
}

func newDLPDatasetDeleteCmd(g *globalOpts) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <dataset-id>",
		Short: "Delete a DLP dataset",
		Long: `Delete a DLP dataset and every uploaded version of it.

Examples:

  cf dlp dataset delete 182bd5e5-6e1a-4fe4-a799-aa6d9a6ab26e --force`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			datasetID, err := dlpRequireID("dataset ID", args[0])
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := dlpAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			if !force && !g.DryRun {
				if !confirm(fmt.Sprintf("Delete DLP dataset %s and all uploaded versions from account %s? Profiles that match on it will stop detecting.", datasetID, accountID)) {
					return errors.New("aborted (pass --force to skip confirmation)")
				}
			}
			req := api.Request{Method: "DELETE", Path: dlpDatasetPath(accountID, datasetID)}
			return runDLPRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	return cmd
}

// --- payload log -----------------------------------------------------------

func newDLPPayloadLogCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "payload-log",
		Short: "Manage DLP payload log settings",
	}
	cmd.AddCommand(
		newDLPPayloadLogGetCmd(g),
		newDLPPayloadLogSetCmd(g),
	)
	return cmd
}

func dlpPayloadLogPath(accountID string) string { return dlpPath(accountID, "/payload_log") }

func newDLPPayloadLogGetCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get",
		Short: "Show DLP payload log settings",
		Long: `Show the account's payload log settings.

public_key is null when payload logging is disabled.

Examples:

  cf dlp payload-log get`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := dlpAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: dlpPayloadLogPath(accountID)}
			return runDLPRequest(cmd, g, client, req)
		},
	}
	return cmd
}

func newDLPPayloadLogSetCmd(g *globalOpts) *cobra.Command {
	var publicKey, maskingLevel string
	var disable bool
	cmd := &cobra.Command{
		Use:   "set",
		Short: "Set DLP payload log settings",
		Long: `Set the payload log public key or masking level.

Payload logging encrypts matched payloads to a public key you hold; generate
the key pair with the Cloudflare payload log CLI and pass the public half here.

A missing public_key means "keep" on some accounts and "clear" on others, so
this command always sends the field: it reads the current settings first,
merges your flags onto them, and writes the complete object. --dry-run
performs that read but never sends the write.

Examples:

  cf dlp payload-log set --public-key ZXhhbXBsZS1wYXlsb2FkLWxvZy1wdWJsaWMta2V5ISE=
  cf dlp payload-log set --masking-level partial
  cf dlp payload-log set --disable`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			patch, err := dlpPayloadLogPatch(cmd, publicKey, maskingLevel, disable)
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := dlpAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			current, err := dlpFetchPayloadLog(cmd.Context(), client, accountID)
			if err != nil {
				return err
			}
			body, err := dlpPayloadLogBody(current, patch)
			if err != nil {
				return err
			}
			req := api.Request{Method: "PUT", Path: dlpPayloadLogPath(accountID), Body: body}
			return runDLPRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&publicKey, "public-key", "", "base64-encoded public key to encrypt payload logs with")
	cmd.Flags().StringVar(&maskingLevel, "masking-level", "", "payload masking level ("+strings.Join(dlpMaskingLevels, ", ")+")")
	cmd.Flags().BoolVar(&disable, "disable", false, "disable payload logging (clears the public key)")
	return cmd
}

// dlpPayloadLogPatch validates the flags and returns the fields to write.
func dlpPayloadLogPatch(cmd *cobra.Command, publicKey, maskingLevel string, disable bool) (map[string]any, error) {
	keySet := cmd.Flags().Changed("public-key")
	disableSet := cmd.Flags().Changed("disable") && disable
	if keySet && disableSet {
		return nil, errors.New("--public-key and --disable cannot be combined")
	}
	patch := map[string]any{}
	switch {
	case keySet:
		if strings.TrimSpace(publicKey) == "" {
			return nil, errors.New("--public-key must not be empty; pass --disable to turn payload logging off")
		}
		if !dlpIsBase64(publicKey) {
			return nil, errors.New("--public-key must be base64-encoded; pass the public half of the key pair as the payload log tool printed it")
		}
		patch["public_key"] = publicKey
	case disableSet:
		patch["public_key"] = nil
	}
	if cmd.Flags().Changed("masking-level") {
		if err := dlpOneOf("--masking-level", maskingLevel, dlpMaskingLevels); err != nil {
			return nil, err
		}
		patch["masking_level"] = maskingLevel
	}
	if len(patch) == 0 {
		return nil, errors.New("nothing to set: pass --public-key, --disable, or --masking-level")
	}
	return patch, nil
}

func dlpFetchPayloadLog(ctx context.Context, client *api.Client, accountID string) (map[string]any, error) {
	env, err := client.Do(ctx, api.Request{Method: "GET", Path: dlpPayloadLogPath(accountID)})
	if err != nil {
		return nil, fmt.Errorf("read payload log settings before update: %w", err)
	}
	var obj map[string]any
	if err := json.Unmarshal(env.Result, &obj); err != nil {
		return nil, errors.New("read payload log settings before update: unexpected response")
	}
	return obj, nil
}

// dlpPayloadLogBody merges the patch onto the current settings. public_key is
// always present in the result so the write is unambiguous on every account.
func dlpPayloadLogBody(current, patch map[string]any) ([]byte, error) {
	body := map[string]any{"public_key": nil}
	if v, ok := current["public_key"]; ok {
		if v == nil {
			body["public_key"] = nil
		} else if s, ok := v.(string); ok {
			body["public_key"] = s
		} else {
			return nil, errors.New("the API returned public_key in an unexpected shape")
		}
	}
	if v, ok := current["masking_level"]; ok && v != nil {
		// Carried through as read: only a level the caller changes is held to
		// the documented enum.
		s, ok := v.(string)
		if !ok {
			return nil, errors.New("the API returned masking_level in an unexpected shape")
		}
		body["masking_level"] = s
	}
	for k, v := range patch {
		body[k] = v
	}
	return json.Marshal(body)
}
