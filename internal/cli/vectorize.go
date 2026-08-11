package cli

// Vectorize porcelain: index lifecycle, bulk vector insert/upsert from an
// NDJSON file, similarity queries, and metadata index management. Targets the
// v2 API (`/vectorize/v2/indexes`); the v1 endpoints are deprecated and are
// reachable via `cf api vectorize` if anyone still needs them.
// See docs/STYLE.md; internal/cli/dns.go is the shape exemplar.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/trmdy/cf-cli/internal/api"
	"github.com/trmdy/cf-cli/internal/output"
)

// vectorizeMetrics are the distance metrics accepted by `create --metric`.
var vectorizeMetrics = []string{"cosine", "euclidean", "dot-product"}

// vectorizePresets are the embedding models `create --preset` understands;
// each one pins the dimensions and metric that model requires.
var vectorizePresets = []string{
	"@cf/baai/bge-small-en-v1.5",
	"@cf/baai/bge-base-en-v1.5",
	"@cf/baai/bge-large-en-v1.5",
	"openai/text-embedding-ada-002",
	"cohere/embed-multilingual-v2.0",
}

// vectorizeMetadataTypes are the metadata property types that can be indexed.
var vectorizeMetadataTypes = []string{"string", "number", "boolean"}

// vectorizeReturnMetadata are the accepted `query --return-metadata` values.
var vectorizeReturnMetadata = []string{"none", "indexed", "all"}

// vectorizeMaxDimensions is the largest index width the API accepts.
const vectorizeMaxDimensions = 1536

// Documented size limits, enforced locally so oversized input fails with a
// message naming the offending value instead of as a 400.
const (
	vectorizeMaxIndexNameBytes = 64
	vectorizeMaxVectorIDBytes  = 64
	vectorizeMaxNamespaceBytes = 64
)

// topK limits: the API returns fewer neighbors per query when each match also
// carries values or metadata.
const (
	vectorizeMaxTopK         = 100
	vectorizeMaxTopKExpanded = 50
)

// vectorizeNDJSONContentType is what the insert/upsert endpoints expect;
// their bodies are newline-delimited JSON, not a JSON document.
const vectorizeNDJSONContentType = "application/x-ndjson"

// vectorizeNamePattern mirrors the index name pattern the API enforces, so a
// typo fails locally with a readable message instead of as a 400.
var vectorizeNamePattern = regexp.MustCompile(`^[a-z]+[a-z0-9_-]*[a-z0-9]+$`)

type vectorizeIndexConfig struct {
	Dimensions int    `json:"dimensions,omitempty"`
	Metric     string `json:"metric,omitempty"`
}

type vectorizeIndex struct {
	Name        string               `json:"name,omitempty"`
	Description string               `json:"description,omitempty"`
	Config      vectorizeIndexConfig `json:"config"`
	CreatedOn   string               `json:"created_on,omitempty"`
	ModifiedOn  string               `json:"modified_on,omitempty"`
}

func newVectorizeCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "vectorize",
		Short: "Manage Vectorize indexes and vectors",
	}
	cmd.AddCommand(
		newVectorizeListCmd(g),
		newVectorizeGetCmd(g),
		newVectorizeCreateCmd(g),
		newVectorizeDeleteCmd(g),
		newVectorizeInsertCmd(g),
		newVectorizeUpsertCmd(g),
		newVectorizeQueryCmd(g),
		newVectorizeMetadataIndexCmd(g),
	)
	return cmd
}

// vectorizeAccountID validates the resolved account scope. Vectorize is
// account-scoped only, so every command needs it.
func vectorizeAccountID(configured string) (string, error) {
	if configured == "" {
		return "", errors.New("no account specified: pass --account-id, set CLOUDFLARE_ACCOUNT_ID, or configure a profile")
	}
	return configured, nil
}

func vectorizeIndexesPath(accountID string) string {
	return "/accounts/" + url.PathEscape(accountID) + "/vectorize/v2/indexes"
}

func vectorizeIndexPath(accountID, name string) string {
	return vectorizeIndexesPath(accountID) + "/" + url.PathEscape(name)
}

func newVectorizeListCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List Vectorize indexes",
		Long:  "List the Vectorize indexes on an account.\n\nExamples:\n\n  cf vectorize list\n  cf vectorize list --output json",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := vectorizeAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: vectorizeIndexesPath(accountID)}
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
			format := g.format(output.Table)
			if g.Query != "" || format != output.Table {
				return g.renderResult(cmd, env.Result, output.JSON)
			}
			var indexes []vectorizeIndex
			if err := json.Unmarshal(env.Result, &indexes); err != nil {
				return output.RenderRaw(cmd.OutOrStdout(), output.JSON, env.Result)
			}
			rows := make([][]string, 0, len(indexes))
			for _, idx := range indexes {
				dims := ""
				if idx.Config.Dimensions > 0 {
					dims = strconv.Itoa(idx.Config.Dimensions)
				}
				rows = append(rows, []string{
					idx.Name,
					dims,
					idx.Config.Metric,
					output.Cell(idx.Description),
					idx.CreatedOn,
				})
			}
			return output.RenderTable(cmd.OutOrStdout(), []string{"NAME", "DIMENSIONS", "METRIC", "DESCRIPTION", "CREATED"}, rows)
		},
	}
	return cmd
}

func newVectorizeGetCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <index>",
		Short: "Show one Vectorize index",
		Long:  "Show the configuration of a Vectorize index.\n\nExamples:\n\n  cf vectorize get product-embeddings",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := vectorizeAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: vectorizeIndexPath(accountID, args[0])}
			return runVectorizeRequest(cmd, g, client, req)
		},
	}
	return cmd
}

// vectorizeCreateOpts carries the flag values for `vectorize create`. Zero
// values mean "not given".
type vectorizeCreateOpts struct {
	name        string
	description string
	metric      string
	preset      string
	dimensions  int
}

func newVectorizeCreateCmd(g *globalOpts) *cobra.Command {
	var opts vectorizeCreateOpts
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a Vectorize index",
		Long: `Create a Vectorize index. Give either --dimensions and --metric, or a
--preset that pins both for a known embedding model.

Examples:

  cf vectorize create product-embeddings --dimensions 768 --metric cosine
  cf vectorize create docs --preset @cf/baai/bge-base-en-v1.5 --description "Docs search"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.name = args[0]
			body, err := buildVectorizeCreateBody(opts)
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := vectorizeAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			req := api.Request{Method: "POST", Path: vectorizeIndexesPath(accountID), Body: body}
			return runVectorizeRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().IntVar(&opts.dimensions, "dimensions", 0, "number of dimensions per vector (1-1536)")
	cmd.Flags().StringVar(&opts.metric, "metric", "", "distance metric: cosine, euclidean, or dot-product")
	cmd.Flags().StringVar(&opts.preset, "preset", "", "embedding model preset that pins dimensions and metric")
	cmd.Flags().StringVar(&opts.description, "description", "", "human-readable index description")
	return cmd
}

// buildVectorizeCreateBody validates the mutually exclusive index config
// modes (explicit dimensions+metric vs. model preset) and returns the body.
func buildVectorizeCreateBody(o vectorizeCreateOpts) ([]byte, error) {
	if err := validateVectorizeIndexName(o.name); err != nil {
		return nil, err
	}
	explicit := o.dimensions != 0 || o.metric != ""
	if o.preset != "" && explicit {
		return nil, errors.New("--preset cannot be combined with --dimensions or --metric")
	}
	if o.preset == "" && !explicit {
		return nil, errors.New("specify the index shape: --dimensions and --metric, or --preset")
	}

	body := map[string]any{"name": o.name}
	if o.description != "" {
		body["description"] = o.description
	}
	if o.preset != "" {
		if !vectorizeContains(vectorizePresets, o.preset) {
			return nil, fmt.Errorf("unknown --preset %q (expected one of: %s)", o.preset, strings.Join(vectorizePresets, ", "))
		}
		body["config"] = map[string]any{"preset": o.preset}
		return json.Marshal(body)
	}
	if o.dimensions == 0 {
		return nil, errors.New("--metric also needs --dimensions (or use --preset)")
	}
	if o.metric == "" {
		return nil, errors.New("--dimensions also needs --metric (or use --preset)")
	}
	if o.dimensions < 1 || o.dimensions > vectorizeMaxDimensions {
		return nil, fmt.Errorf("--dimensions must be between 1 and %d, got %d", vectorizeMaxDimensions, o.dimensions)
	}
	if !vectorizeContains(vectorizeMetrics, o.metric) {
		return nil, fmt.Errorf("unknown --metric %q (expected one of: %s)", o.metric, strings.Join(vectorizeMetrics, ", "))
	}
	body["config"] = map[string]any{"dimensions": o.dimensions, "metric": o.metric}
	return json.Marshal(body)
}

// validateVectorizeIndexName rejects names the API would reject, so create
// fails with a readable message instead of a 400.
func validateVectorizeIndexName(name string) error {
	if name == "" {
		return errors.New("index name is required")
	}
	if !vectorizeNamePattern.MatchString(name) {
		return fmt.Errorf("invalid index name %q: use lowercase letters, digits, dashes, and underscores, starting with a letter and ending with a letter or digit", name)
	}
	if len(name) > vectorizeMaxIndexNameBytes {
		return fmt.Errorf("index name is %d bytes; the limit is %d", len(name), vectorizeMaxIndexNameBytes)
	}
	return nil
}

func newVectorizeDeleteCmd(g *globalOpts) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <index>",
		Short: "Delete a Vectorize index",
		Long:  "Delete a Vectorize index and every vector in it.\n\nExamples:\n\n  cf vectorize delete product-embeddings --force",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := vectorizeAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			if !force && !g.DryRun {
				if !confirm(fmt.Sprintf("Delete Vectorize index %s and all of its vectors?", args[0])) {
					return errors.New("aborted (pass --force to skip confirmation)")
				}
			}
			req := api.Request{Method: "DELETE", Path: vectorizeIndexPath(accountID, args[0])}
			return runVectorizeRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	return cmd
}

func newVectorizeInsertCmd(g *globalOpts) *cobra.Command {
	return newVectorizeWriteCmd(g, "insert", "Insert vectors, failing on IDs that already exist")
}

func newVectorizeUpsertCmd(g *globalOpts) *cobra.Command {
	return newVectorizeWriteCmd(g, "upsert", "Upsert vectors, overwriting IDs that already exist")
}

// newVectorizeWriteCmd builds `insert` and `upsert`, which differ only in the
// endpoint and in what happens to vector IDs that already exist.
func newVectorizeWriteCmd(g *globalOpts, verb, short string) *cobra.Command {
	var data, unparsable string
	cmd := &cobra.Command{
		Use:   verb + " <index>",
		Short: short,
		Long: fmt.Sprintf(`%s.

Vectors are read as NDJSON (one JSON object per line) from --data, which
accepts an inline string, @file, or @- for stdin. A plain JSON array of
vector objects is accepted too and converted to NDJSON. Each vector needs an
"id" and a numeric "values" array; "metadata" and "namespace" are optional.

Vectors are checked locally first, so a bad one is reported by position. With
--unparsable-behavior discard you have asked the API to drop what it cannot
parse, so that check is skipped and every vector is sent as given.

Examples:

  cf vectorize %s product-embeddings --data @vectors.ndjson
  cf vectorize %s product-embeddings --data @- < vectors.ndjson
  cf vectorize %s product-embeddings --data @vectors.ndjson --unparsable-behavior discard`,
			short, verb, verb, verb),
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			q := url.Values{}
			discard := false
			if cmd.Flags().Changed("unparsable-behavior") {
				if unparsable != "error" && unparsable != "discard" {
					return fmt.Errorf("unknown --unparsable-behavior %q (expected error or discard)", unparsable)
				}
				discard = unparsable == "discard"
				q.Set("unparsable-behavior", unparsable)
			}
			raw, err := vectorizeReadArg("data", data, cmd.InOrStdin())
			if err != nil {
				return err
			}
			body, err := buildVectorizeVectorsBody(raw, discard)
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := vectorizeAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			req := api.Request{
				Method:      "POST",
				Path:        vectorizeIndexPath(accountID, args[0]) + "/" + verb,
				Query:       q,
				Body:        body,
				ContentType: vectorizeNDJSONContentType,
			}
			return runVectorizeRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&data, "data", "", "NDJSON vectors: inline string, @file, or @- for stdin")
	cmd.Flags().StringVar(&unparsable, "unparsable-behavior", "error", "what to do with vectors the API cannot parse: error or discard")
	_ = cmd.MarkFlagRequired("data")
	return cmd
}

// buildVectorizeVectorsBody normalizes vectors into NDJSON. It accepts either
// NDJSON already (blank lines ignored) or a single JSON array of objects.
//
// Each vector is validated so a bad one is reported by position rather than as
// an opaque API error. When discard is set the caller passed
// --unparsable-behavior discard, which is a request for the API to drop
// vectors it cannot parse; rejecting them here instead would defeat that, so
// vectors are passed through unchecked and the API decides.
func buildVectorizeVectorsBody(raw []byte, discard bool) ([]byte, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return nil, errors.New("no vectors given: --data was empty")
	}

	var lines []string
	if strings.HasPrefix(trimmed, "[") {
		var items []json.RawMessage
		if err := json.Unmarshal([]byte(trimmed), &items); err != nil {
			return nil, fmt.Errorf("--data looks like a JSON array but is not valid JSON: %w", err)
		}
		for _, item := range items {
			lines = append(lines, string(item))
		}
	} else {
		for _, line := range strings.Split(trimmed, "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			lines = append(lines, strings.TrimSpace(line))
		}
	}
	if len(lines) == 0 {
		return nil, errors.New("no vectors given: --data contained no vector objects")
	}

	var b strings.Builder
	for i, line := range lines {
		out := line
		if discard {
			// Keep unparsable input intact for the API to drop; still collapse
			// well-formed vectors so one array element cannot span two lines.
			out = vectorizeCompactLine(line)
		} else {
			compact, err := validateVectorizeVector(line, i+1)
			if err != nil {
				return nil, err
			}
			out = compact
		}
		b.WriteString(out)
		b.WriteByte('\n')
	}
	return []byte(b.String()), nil
}

// vectorizeCompactLine collapses a JSON value onto one line, returning the
// input untouched when it is not valid JSON.
func vectorizeCompactLine(line string) string {
	var compact bytes.Buffer
	if err := json.Compact(&compact, []byte(line)); err != nil {
		return line
	}
	return compact.String()
}

// validateVectorizeVector checks one vector object and returns it compacted
// onto a single NDJSON line.
func validateVectorizeVector(line string, pos int) (string, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(line), &obj); err != nil {
		return "", fmt.Errorf("vector %d is not a JSON object: %w", pos, err)
	}

	var id string
	raw, ok := obj["id"]
	if !ok || json.Unmarshal(raw, &id) != nil || id == "" {
		return "", fmt.Errorf("vector %d is missing a non-empty %q string", pos, "id")
	}
	if len(id) > vectorizeMaxVectorIDBytes {
		return "", fmt.Errorf("vector %d has an %q of %d bytes; the limit is %d", pos, "id", len(id), vectorizeMaxVectorIDBytes)
	}

	raw, ok = obj["values"]
	if !ok {
		return "", fmt.Errorf("vector %d (id %q) is missing a non-empty %q array", pos, id, "values")
	}
	var values []json.Number
	if err := json.Unmarshal(raw, &values); err != nil {
		return "", fmt.Errorf("vector %d (id %q) has a %q that is not an array of numbers: %w", pos, id, "values", err)
	}
	if len(values) == 0 {
		return "", fmt.Errorf("vector %d (id %q) is missing a non-empty %q array", pos, id, "values")
	}

	if raw, ok := obj["namespace"]; ok && string(raw) != "null" {
		var ns string
		if err := json.Unmarshal(raw, &ns); err != nil {
			return "", fmt.Errorf("vector %d (id %q) has a %q that is not a string", pos, id, "namespace")
		}
		if len(ns) > vectorizeMaxNamespaceBytes {
			return "", fmt.Errorf("vector %d (id %q) has a %q of %d bytes; the limit is %d", pos, id, "namespace", len(ns), vectorizeMaxNamespaceBytes)
		}
	}

	var compact bytes.Buffer
	if err := json.Compact(&compact, []byte(line)); err != nil {
		return "", fmt.Errorf("vector %d is not valid JSON: %w", pos, err)
	}
	return compact.String(), nil
}

// vectorizeQueryOpts carries the flag values for `vectorize query`.
type vectorizeQueryOpts struct {
	vector            string
	filter            string
	topK              int
	returnValues      bool
	returnMetadata    string
	topKSet           bool
	returnValuesSet   bool
	returnMetadataSet bool
	stdin             io.Reader
}

func newVectorizeQueryCmd(g *globalOpts) *cobra.Command {
	var opts vectorizeQueryOpts
	cmd := &cobra.Command{
		Use:   "query <index>",
		Short: "Find the vectors nearest to a search vector",
		Long: `Find the nearest neighbors of a search vector.

--vector takes a JSON array of numbers and --filter a JSON metadata filter
object; both accept an inline string, @file, or @- for stdin.

Examples:

  cf vectorize query product-embeddings --vector '[0.1,0.2,0.3]' --top-k 10
  cf vectorize query product-embeddings --vector @embedding.json --filter '{"genre":{"$eq":"drama"}}'
  cf vectorize query product-embeddings --vector @embedding.json --return-values --return-metadata all`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.topKSet = cmd.Flags().Changed("top-k")
			opts.returnValuesSet = cmd.Flags().Changed("return-values")
			opts.returnMetadataSet = cmd.Flags().Changed("return-metadata")
			opts.stdin = cmd.InOrStdin()
			body, err := buildVectorizeQueryBody(opts)
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := vectorizeAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			req := api.Request{Method: "POST", Path: vectorizeIndexPath(accountID, args[0]) + "/query", Body: body}
			return runVectorizeRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&opts.vector, "vector", "", "search vector as a JSON array of numbers: inline, @file, or @- for stdin")
	cmd.Flags().StringVar(&opts.filter, "filter", "", "metadata filter as a JSON object: inline, @file, or @- for stdin")
	cmd.Flags().IntVar(&opts.topK, "top-k", 5, "number of nearest neighbors to return (max 100, or 50 with values or metadata)")
	cmd.Flags().BoolVar(&opts.returnValues, "return-values", false, "include vector values in the results")
	cmd.Flags().StringVar(&opts.returnMetadata, "return-metadata", "none", "metadata to include: none, indexed, or all")
	_ = cmd.MarkFlagRequired("vector")
	return cmd
}

// buildVectorizeQueryBody resolves --vector/--filter (inline, @file, or @-)
// and assembles the query body, sending only the options the user set.
func buildVectorizeQueryBody(o vectorizeQueryOpts) ([]byte, error) {
	if o.vector == "@-" && o.filter == "@-" {
		return nil, errors.New("--vector and --filter cannot both read stdin (@-)")
	}
	rawVector, err := vectorizeReadArg("vector", o.vector, o.stdin)
	if err != nil {
		return nil, err
	}
	var values []json.Number
	if err := json.Unmarshal(rawVector, &values); err != nil {
		return nil, fmt.Errorf("--vector must be a JSON array of numbers: %w", err)
	}
	if len(values) == 0 {
		return nil, errors.New("--vector must contain at least one number")
	}

	body := map[string]any{"vector": values}
	if o.filter != "" {
		rawFilter, err := vectorizeReadArg("filter", o.filter, o.stdin)
		if err != nil {
			return nil, err
		}
		var filter any
		if err := json.Unmarshal(rawFilter, &filter); err != nil {
			return nil, fmt.Errorf("--filter must be a JSON object: %w", err)
		}
		// null, arrays, and scalars all decode without error, so check the
		// shape rather than trusting the decode.
		obj, ok := filter.(map[string]any)
		if !ok {
			return nil, errors.New(`--filter must be a JSON object, for example '{"genre":{"$eq":"drama"}}'`)
		}
		body["filter"] = obj
	}
	if o.returnMetadataSet {
		if !vectorizeContains(vectorizeReturnMetadata, o.returnMetadata) {
			return nil, fmt.Errorf("unknown --return-metadata %q (expected one of: %s)", o.returnMetadata, strings.Join(vectorizeReturnMetadata, ", "))
		}
		body["returnMetadata"] = o.returnMetadata
	}
	if o.returnValuesSet {
		body["returnValues"] = o.returnValues
	}
	if o.topKSet {
		if err := validateVectorizeTopK(o); err != nil {
			return nil, err
		}
		body["topK"] = o.topK
	}
	return json.Marshal(body)
}

// validateVectorizeTopK enforces the neighbor limit, which is lower when each
// match also carries values or metadata.
func validateVectorizeTopK(o vectorizeQueryOpts) error {
	if o.topK < 1 {
		return fmt.Errorf("--top-k must be at least 1, got %d", o.topK)
	}
	expanded := (o.returnValuesSet && o.returnValues) ||
		(o.returnMetadataSet && (o.returnMetadata == "indexed" || o.returnMetadata == "all"))
	if expanded {
		if o.topK > vectorizeMaxTopKExpanded {
			return fmt.Errorf("--top-k must be at most %d when --return-values is set or --return-metadata is indexed or all, got %d",
				vectorizeMaxTopKExpanded, o.topK)
		}
		return nil
	}
	if o.topK > vectorizeMaxTopK {
		return fmt.Errorf("--top-k must be at most %d, got %d", vectorizeMaxTopK, o.topK)
	}
	return nil
}

func newVectorizeMetadataIndexCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "metadata-index",
		Short: "Manage indexed metadata properties of an index",
	}
	cmd.AddCommand(
		newVectorizeMetadataIndexListCmd(g),
		newVectorizeMetadataIndexCreateCmd(g),
		newVectorizeMetadataIndexDeleteCmd(g),
	)
	return cmd
}

func vectorizeMetadataIndexPath(accountID, name, action string) string {
	return vectorizeIndexPath(accountID, name) + "/metadata_index/" + action
}

type vectorizeMetadataIndexList struct {
	MetadataIndexes []struct {
		PropertyName string `json:"propertyName"`
		IndexType    string `json:"indexType"`
	} `json:"metadataIndexes"`
}

func newVectorizeMetadataIndexListCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list <index>",
		Short: "List indexed metadata properties",
		Long:  "List the metadata properties that are indexed for filtering.\n\nExamples:\n\n  cf vectorize metadata-index list product-embeddings",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := vectorizeAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: vectorizeMetadataIndexPath(accountID, args[0], "list")}
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
			format := g.format(output.Table)
			if g.Query != "" || format != output.Table {
				return g.renderResult(cmd, env.Result, output.JSON)
			}
			var result vectorizeMetadataIndexList
			if err := json.Unmarshal(env.Result, &result); err != nil {
				return output.RenderRaw(cmd.OutOrStdout(), output.JSON, env.Result)
			}
			rows := make([][]string, 0, len(result.MetadataIndexes))
			for _, mi := range result.MetadataIndexes {
				rows = append(rows, []string{mi.PropertyName, mi.IndexType})
			}
			return output.RenderTable(cmd.OutOrStdout(), []string{"PROPERTY", "TYPE"}, rows)
		},
	}
	return cmd
}

func newVectorizeMetadataIndexCreateCmd(g *globalOpts) *cobra.Command {
	var property, propertyType string
	cmd := &cobra.Command{
		Use:   "create <index>",
		Short: "Index a metadata property for filtering",
		Long: `Index a metadata property so queries can filter on it.

Examples:

  cf vectorize metadata-index create product-embeddings --property genre --type string
  cf vectorize metadata-index create product-embeddings --property price --type number`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := buildVectorizeMetadataIndexBody(property, propertyType)
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := vectorizeAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			req := api.Request{Method: "POST", Path: vectorizeMetadataIndexPath(accountID, args[0], "create"), Body: body}
			return runVectorizeRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&property, "property", "", "metadata property to index")
	cmd.Flags().StringVar(&propertyType, "type", "", "property type: string, number, or boolean")
	_ = cmd.MarkFlagRequired("property")
	_ = cmd.MarkFlagRequired("type")
	return cmd
}

// buildVectorizeMetadataIndexBody validates the property/type pair for
// `metadata-index create`.
func buildVectorizeMetadataIndexBody(property, propertyType string) ([]byte, error) {
	if strings.TrimSpace(property) == "" {
		return nil, errors.New("--property must not be empty")
	}
	if !vectorizeContains(vectorizeMetadataTypes, propertyType) {
		return nil, fmt.Errorf("unknown --type %q (expected one of: %s)", propertyType, strings.Join(vectorizeMetadataTypes, ", "))
	}
	return json.Marshal(map[string]string{"propertyName": property, "indexType": propertyType})
}

func newVectorizeMetadataIndexDeleteCmd(g *globalOpts) *cobra.Command {
	var property string
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <index>",
		Short: "Stop indexing a metadata property",
		Long:  "Delete the metadata index for a property; queries can no longer filter on it.\n\nExamples:\n\n  cf vectorize metadata-index delete product-embeddings --property genre --force",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(property) == "" {
				return errors.New("--property must not be empty")
			}
			body, err := json.Marshal(map[string]string{"propertyName": property})
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := vectorizeAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			if !force && !g.DryRun {
				if !confirm(fmt.Sprintf("Delete the metadata index for %q on Vectorize index %s?", property, args[0])) {
					return errors.New("aborted (pass --force to skip confirmation)")
				}
			}
			req := api.Request{Method: "POST", Path: vectorizeMetadataIndexPath(accountID, args[0], "delete"), Body: body}
			return runVectorizeRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&property, "property", "", "metadata property to stop indexing")
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	_ = cmd.MarkFlagRequired("property")
	return cmd
}

// vectorizeReadArg resolves a flag value that may be inline, @file, or @- for
// stdin. Errors name the flag so they say how to fix the invocation.
func vectorizeReadArg(flag, value string, stdin io.Reader) ([]byte, error) {
	switch {
	case value == "":
		return nil, fmt.Errorf("--%s is required", flag)
	case value == "@-":
		raw, err := io.ReadAll(stdin)
		if err != nil {
			return nil, fmt.Errorf("read --%s from stdin: %w", flag, err)
		}
		return raw, nil
	case strings.HasPrefix(value, "@"):
		path := strings.TrimPrefix(value, "@")
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read --%s from %s: %w", flag, path, err)
		}
		return raw, nil
	default:
		return []byte(value), nil
	}
}

func vectorizeContains(allowed []string, v string) bool {
	for _, a := range allowed {
		if a == v {
			return true
		}
	}
	return false
}

func runVectorizeRequest(cmd *cobra.Command, g *globalOpts, client *api.Client, req api.Request) error {
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
