package cli

// KV porcelain: namespace and key workflows for Workers KV.
// See docs/STYLE.md; internal/cli/dns.go is the shape exemplar.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/trmdy/cf-cli/internal/api"
	"github.com/trmdy/cf-cli/internal/output"
)

type kvNamespace struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

type kvKey struct {
	Name       string          `json:"name"`
	Expiration int64           `json:"expiration,omitempty"`
	Metadata   json.RawMessage `json:"metadata,omitempty"`
}

func newKVCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "kv",
		Short: "Manage Workers KV namespaces and keys",
	}
	cmd.AddCommand(
		newKVNamespaceCmd(g),
		newKVKeyCmd(g),
	)
	return cmd
}

func newKVNamespaceCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "namespace",
		Short: "Manage KV namespaces",
	}
	cmd.AddCommand(
		newKVNamespaceListCmd(g),
		newKVNamespaceCreateCmd(g),
		newKVNamespaceDeleteCmd(g),
		newKVNamespaceRenameCmd(g),
	)
	return cmd
}

func newKVKeyCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "key",
		Short: "Manage keys inside a KV namespace",
	}
	cmd.AddCommand(
		newKVKeyListCmd(g),
		newKVKeyGetCmd(g),
		newKVKeyPutCmd(g),
		newKVKeyDeleteCmd(g),
		newKVKeyBulkPutCmd(g),
		newKVKeyBulkDeleteCmd(g),
	)
	return cmd
}

func kvAccountID(accountID string) (string, error) {
	if strings.TrimSpace(accountID) == "" {
		return "", errors.New("no account specified: pass --account-id, set CLOUDFLARE_ACCOUNT_ID, or configure a profile")
	}
	return accountID, nil
}

func kvNamespacesPath(accountID string) string {
	return "/accounts/" + url.PathEscape(accountID) + "/storage/kv/namespaces"
}

func kvNamespacePath(accountID, namespaceID string) string {
	return kvNamespacesPath(accountID) + "/" + url.PathEscape(namespaceID)
}

func kvKeysPath(accountID, namespaceID string) string {
	return kvNamespacePath(accountID, namespaceID) + "/keys"
}

func kvValuePath(accountID, namespaceID, key string) string {
	return kvNamespacePath(accountID, namespaceID) + "/values/" + url.PathEscape(key)
}

func kvBulkPath(accountID, namespaceID string) string {
	return kvNamespacePath(accountID, namespaceID) + "/bulk"
}

func kvBulkDeletePath(accountID, namespaceID string) string {
	return kvNamespacePath(accountID, namespaceID) + "/bulk/delete"
}

// resolveKVNamespaceID accepts a namespace ID (32 hex chars) or a namespace title
// looked up via the list API.
func resolveKVNamespaceID(ctx context.Context, client *api.Client, accountID, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", errors.New("no namespace specified: pass a namespace ID or title")
	}
	if isZoneID(ref) {
		return ref, nil
	}
	req := api.Request{
		Method: "GET",
		Path:   kvNamespacesPath(accountID),
		Query:  url.Values{"per_page": {"100"}},
	}
	env, err := client.DoAutoPaginate(ctx, req)
	if err != nil {
		return "", fmt.Errorf("look up namespace %q: %w", ref, err)
	}
	var namespaces []kvNamespace
	if err := json.Unmarshal(env.Result, &namespaces); err != nil {
		return "", fmt.Errorf("look up namespace %q: unexpected response", ref)
	}
	var matches []kvNamespace
	for _, ns := range namespaces {
		if ns.Title == ref || ns.ID == ref {
			matches = append(matches, ns)
		}
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("namespace %q not found on this account", ref)
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("namespace title %q is ambiguous (%d matches); pass the namespace ID", ref, len(matches))
	}
	return matches[0].ID, nil
}

func newKVNamespaceListCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List KV namespaces",
		Long:  "List Workers KV namespaces in the configured account.\n\nExamples:\n\n  cf kv namespace list\n  cf kv namespace list --account-id <account-id>",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := kvAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			req := api.Request{
				Method: "GET",
				Path:   kvNamespacesPath(accountID),
				Query:  url.Values{"per_page": {"100"}},
			}
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
			var namespaces []kvNamespace
			if err := json.Unmarshal(env.Result, &namespaces); err != nil {
				return output.RenderRaw(cmd.OutOrStdout(), output.JSON, env.Result)
			}
			rows := make([][]string, 0, len(namespaces))
			for _, ns := range namespaces {
				rows = append(rows, []string{ns.ID, ns.Title})
			}
			return output.RenderTable(cmd.OutOrStdout(), []string{"ID", "TITLE"}, rows)
		},
	}
	return cmd
}

func newKVNamespaceCreateCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create <title>",
		Short: "Create a KV namespace",
		Long:  "Create a Workers KV namespace.\n\nExamples:\n\n  cf kv namespace create my-app-config\n  cf kv namespace create staging-flags",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			title := strings.TrimSpace(args[0])
			if title == "" {
				return errors.New("namespace title cannot be empty")
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := kvAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			body, err := json.Marshal(map[string]string{"title": title})
			if err != nil {
				return err
			}
			req := api.Request{Method: "POST", Path: kvNamespacesPath(accountID), Body: body}
			return runKVRequest(cmd, g, client, req)
		},
	}
	return cmd
}

func newKVNamespaceDeleteCmd(g *globalOpts) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <namespace>",
		Short: "Delete a KV namespace",
		Long:  "Delete a Workers KV namespace by ID or title.\n\nExamples:\n\n  cf kv namespace delete 0f2ac74b498b48028cb68387c421e279 --force\n  cf kv namespace delete my-app-config --force",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := kvAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			namespaceID, err := resolveKVNamespaceID(cmd.Context(), client, accountID, args[0])
			if err != nil {
				return err
			}
			if !force && !g.DryRun {
				if !confirm(fmt.Sprintf("Delete KV namespace %s?", namespaceID)) {
					return errors.New("aborted (pass --force to skip confirmation)")
				}
			}
			req := api.Request{Method: "DELETE", Path: kvNamespacePath(accountID, namespaceID)}
			return runKVRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	return cmd
}

func newKVNamespaceRenameCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rename <namespace> <new-title>",
		Short: "Rename a KV namespace",
		Long:  "Rename a Workers KV namespace.\n\nExamples:\n\n  cf kv namespace rename 0f2ac74b498b48028cb68387c421e279 production-config\n  cf kv namespace rename old-title new-title",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			title := strings.TrimSpace(args[1])
			if title == "" {
				return errors.New("new namespace title cannot be empty")
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := kvAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			namespaceID, err := resolveKVNamespaceID(cmd.Context(), client, accountID, args[0])
			if err != nil {
				return err
			}
			body, err := json.Marshal(map[string]string{"title": title})
			if err != nil {
				return err
			}
			req := api.Request{Method: "PUT", Path: kvNamespacePath(accountID, namespaceID), Body: body}
			return runKVRequest(cmd, g, client, req)
		},
	}
	return cmd
}

func newKVKeyListCmd(g *globalOpts) *cobra.Command {
	var namespace, prefix string
	var limit int
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List keys in a KV namespace",
		Long:  "List keys in a Workers KV namespace.\n\nExamples:\n\n  cf kv key list --namespace 0f2ac74b498b48028cb68387c421e279\n  cf kv key list --namespace my-app-config --prefix user:\n  cf kv key list --namespace my-app-config --limit 100",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := kvAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			namespaceID, err := resolveKVNamespaceID(cmd.Context(), client, accountID, namespace)
			if err != nil {
				return err
			}
			q := url.Values{}
			if prefix != "" {
				q.Set("prefix", prefix)
			}
			if cmd.Flags().Changed("limit") {
				if limit < 1 {
					return errors.New("--limit must be at least 1")
				}
				q.Set("limit", strconv.Itoa(limit))
			}
			req := api.Request{Method: "GET", Path: kvKeysPath(accountID, namespaceID), Query: q}
			if g.DryRun {
				dump, err := client.Dump(req)
				if err != nil {
					return err
				}
				return g.renderValue(cmd, dump, output.JSON)
			}
			// Cursor pagination: DoAutoPaginate follows result_info.cursor.
			// A pinned limit means the caller wants a single page only.
			var env *api.Envelope
			if cmd.Flags().Changed("limit") {
				env, err = client.Do(cmd.Context(), req)
			} else {
				env, err = client.DoAutoPaginate(cmd.Context(), req)
			}
			if err != nil {
				return err
			}
			format := g.format(output.Table)
			if g.Query != "" || format != output.Table {
				return g.renderResult(cmd, env.Result, output.JSON)
			}
			var keys []kvKey
			if err := json.Unmarshal(env.Result, &keys); err != nil {
				return output.RenderRaw(cmd.OutOrStdout(), output.JSON, env.Result)
			}
			rows := make([][]string, 0, len(keys))
			for _, k := range keys {
				exp := ""
				if k.Expiration > 0 {
					exp = strconv.FormatInt(k.Expiration, 10)
				}
				meta := ""
				if len(k.Metadata) > 0 && string(k.Metadata) != "null" {
					meta = output.Cell(string(k.Metadata))
				}
				rows = append(rows, []string{k.Name, exp, meta})
			}
			return output.RenderTable(cmd.OutOrStdout(), []string{"NAME", "EXPIRATION", "METADATA"}, rows)
		},
	}
	cmd.Flags().StringVar(&namespace, "namespace", "", "namespace ID or title")
	cmd.Flags().StringVar(&prefix, "prefix", "", "only list keys with this prefix")
	cmd.Flags().IntVar(&limit, "limit", 0, "maximum keys to return in one request (disables auto-pagination)")
	_ = cmd.MarkFlagRequired("namespace")
	return cmd
}

func newKVKeyGetCmd(g *globalOpts) *cobra.Command {
	var namespace string
	cmd := &cobra.Command{
		Use:   "get <key>",
		Short: "Read a KV key value",
		Long:  "Read the value of a key in a Workers KV namespace.\n\nExamples:\n\n  cf kv key get session:abc --namespace my-app-config\n  cf kv key get config.json --namespace 0f2ac74b498b48028cb68387c421e279",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			key := args[0]
			if strings.TrimSpace(key) == "" {
				return errors.New("key name cannot be empty")
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := kvAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			namespaceID, err := resolveKVNamespaceID(cmd.Context(), client, accountID, namespace)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: kvValuePath(accountID, namespaceID, key)}
			return runKVRawRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&namespace, "namespace", "", "namespace ID or title")
	_ = cmd.MarkFlagRequired("namespace")
	return cmd
}

func newKVKeyPutCmd(g *globalOpts) *cobra.Command {
	var namespace, value string
	var ttl, expiration int64
	cmd := &cobra.Command{
		Use:   "put <key>",
		Short: "Write a KV key value",
		Long:  "Write a value for a key in a Workers KV namespace.\n\nThe value is the request body (text). Use --value @file to read from a file,\nor --value @- to read from stdin.\n\nExamples:\n\n  cf kv key put greeting --namespace my-app-config --value hello\n  cf kv key put config.json --namespace my-app-config --value @./config.json\n  cf kv key put session:abc --namespace my-app-config --value token --ttl 3600",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			key := args[0]
			if strings.TrimSpace(key) == "" {
				return errors.New("key name cannot be empty")
			}
			if !cmd.Flags().Changed("value") {
				return errors.New("missing --value: pass a string, @file, or @- for stdin")
			}
			body, err := kvReadValue(value, cmd.InOrStdin())
			if err != nil {
				return err
			}
			if cmd.Flags().Changed("ttl") && cmd.Flags().Changed("expiration") {
				return errors.New("specify only one of --ttl or --expiration")
			}
			if cmd.Flags().Changed("ttl") && ttl < 60 {
				return errors.New("--ttl must be at least 60 seconds")
			}
			if cmd.Flags().Changed("expiration") && expiration <= 0 {
				return errors.New("--expiration must be a positive UNIX timestamp")
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := kvAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			namespaceID, err := resolveKVNamespaceID(cmd.Context(), client, accountID, namespace)
			if err != nil {
				return err
			}
			q := url.Values{}
			if cmd.Flags().Changed("ttl") {
				q.Set("expiration_ttl", strconv.FormatInt(ttl, 10))
			}
			if cmd.Flags().Changed("expiration") {
				q.Set("expiration", strconv.FormatInt(expiration, 10))
			}
			req := api.Request{
				Method:      "PUT",
				Path:        kvValuePath(accountID, namespaceID, key),
				Query:       q,
				Body:        body,
				ContentType: "text/plain",
			}
			return runKVRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&namespace, "namespace", "", "namespace ID or title")
	cmd.Flags().StringVar(&value, "value", "", "value to store (inline string, @file, or @- for stdin)")
	cmd.Flags().Int64Var(&ttl, "ttl", 0, "expire the key after this many seconds (minimum 60)")
	cmd.Flags().Int64Var(&expiration, "expiration", 0, "expire the key at this UNIX timestamp")
	_ = cmd.MarkFlagRequired("namespace")
	_ = cmd.MarkFlagRequired("value")
	return cmd
}

func newKVKeyDeleteCmd(g *globalOpts) *cobra.Command {
	var namespace string
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <key>",
		Short: "Delete a KV key",
		Long:  "Delete a key from a Workers KV namespace.\n\nExamples:\n\n  cf kv key delete session:abc --namespace my-app-config --force",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			key := args[0]
			if strings.TrimSpace(key) == "" {
				return errors.New("key name cannot be empty")
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := kvAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			namespaceID, err := resolveKVNamespaceID(cmd.Context(), client, accountID, namespace)
			if err != nil {
				return err
			}
			if !force && !g.DryRun {
				if !confirm(fmt.Sprintf("Delete KV key %q from namespace %s?", key, namespaceID)) {
					return errors.New("aborted (pass --force to skip confirmation)")
				}
			}
			req := api.Request{Method: "DELETE", Path: kvValuePath(accountID, namespaceID, key)}
			return runKVRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&namespace, "namespace", "", "namespace ID or title")
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	_ = cmd.MarkFlagRequired("namespace")
	return cmd
}

func newKVKeyBulkPutCmd(g *globalOpts) *cobra.Command {
	var namespace string
	cmd := &cobra.Command{
		Use:   "bulk-put <@file>",
		Short: "Write many KV keys from a JSON file",
		Long: `Write multiple key-value pairs from a JSON file.

The file must be a JSON array of objects with at least "key" and "value".
Optional fields: base64, expiration, expiration_ttl, metadata.

Examples:

  cf kv key bulk-put @pairs.json --namespace my-app-config
  cf kv key bulk-put @- --namespace my-app-config   # read JSON from stdin`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := kvReadBulkFile(args[0], cmd.InOrStdin())
			if err != nil {
				return err
			}
			if err := kvValidateBulkPutBody(body); err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := kvAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			namespaceID, err := resolveKVNamespaceID(cmd.Context(), client, accountID, namespace)
			if err != nil {
				return err
			}
			req := api.Request{Method: "PUT", Path: kvBulkPath(accountID, namespaceID), Body: body}
			return runKVRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&namespace, "namespace", "", "namespace ID or title")
	_ = cmd.MarkFlagRequired("namespace")
	return cmd
}

func newKVKeyBulkDeleteCmd(g *globalOpts) *cobra.Command {
	var namespace string
	var force bool
	cmd := &cobra.Command{
		Use:   "bulk-delete <@file>",
		Short: "Delete many KV keys listed in a JSON file",
		Long: `Delete multiple keys listed in a JSON file.

The file must be a JSON array of key name strings (up to 10,000).

Examples:

  cf kv key bulk-delete @keys.json --namespace my-app-config --force
  cf kv key bulk-delete @- --namespace my-app-config --force`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := kvReadBulkFile(args[0], cmd.InOrStdin())
			if err != nil {
				return err
			}
			keys, err := kvValidateBulkDeleteBody(body)
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := kvAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			namespaceID, err := resolveKVNamespaceID(cmd.Context(), client, accountID, namespace)
			if err != nil {
				return err
			}
			if !force && !g.DryRun {
				if !confirm(fmt.Sprintf("Delete %d KV key(s) from namespace %s?", len(keys), namespaceID)) {
					return errors.New("aborted (pass --force to skip confirmation)")
				}
			}
			req := api.Request{Method: "POST", Path: kvBulkDeletePath(accountID, namespaceID), Body: body}
			return runKVRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&namespace, "namespace", "", "namespace ID or title")
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	_ = cmd.MarkFlagRequired("namespace")
	return cmd
}

// kvReadValue loads a put value: inline string, @file, or @- for stdin.
func kvReadValue(spec string, stdin io.Reader) ([]byte, error) {
	switch {
	case spec == "@-":
		data, err := io.ReadAll(stdin)
		if err != nil {
			return nil, fmt.Errorf("read value from stdin: %w", err)
		}
		return data, nil
	case strings.HasPrefix(spec, "@"):
		path := strings.TrimPrefix(spec, "@")
		if path == "" {
			return nil, errors.New("--value @file path is empty")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read value file %q: %w", path, err)
		}
		return data, nil
	default:
		return []byte(spec), nil
	}
}

// kvReadBulkFile requires an @file (or @-) argument and returns its contents.
func kvReadBulkFile(spec string, stdin io.Reader) ([]byte, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, errors.New("missing @file argument (pass @path.json or @- for stdin)")
	}
	if !strings.HasPrefix(spec, "@") {
		return nil, fmt.Errorf("bulk input must be @file or @- (got %q)", spec)
	}
	if spec == "@-" {
		data, err := io.ReadAll(stdin)
		if err != nil {
			return nil, fmt.Errorf("read bulk input from stdin: %w", err)
		}
		if len(bytesTrimSpace(data)) == 0 {
			return nil, errors.New("bulk input from stdin is empty")
		}
		if !json.Valid(data) {
			return nil, errors.New("bulk input from stdin is not valid JSON")
		}
		return data, nil
	}
	path := strings.TrimPrefix(spec, "@")
	if path == "" {
		return nil, errors.New("@file path is empty")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read bulk file %q: %w", path, err)
	}
	if len(bytesTrimSpace(data)) == 0 {
		return nil, fmt.Errorf("bulk file %q is empty", path)
	}
	if !json.Valid(data) {
		return nil, fmt.Errorf("bulk file %q is not valid JSON", path)
	}
	return data, nil
}

func bytesTrimSpace(b []byte) []byte {
	return []byte(strings.TrimSpace(string(b)))
}

func kvValidateBulkPutBody(body []byte) error {
	var items []map[string]any
	if err := json.Unmarshal(body, &items); err != nil {
		return errors.New("bulk put file must be a JSON array of objects with key and value")
	}
	if len(items) == 0 {
		return errors.New("bulk put file must contain at least one key-value pair")
	}
	if len(items) > 10000 {
		return fmt.Errorf("bulk put supports at most 10000 pairs (got %d)", len(items))
	}
	for i, item := range items {
		key, _ := item["key"].(string)
		if strings.TrimSpace(key) == "" {
			return fmt.Errorf("bulk put item %d: missing non-empty string \"key\"", i)
		}
		if _, ok := item["value"]; !ok {
			return fmt.Errorf("bulk put item %d (key %q): missing \"value\"", i, key)
		}
		switch item["value"].(type) {
		case string:
		default:
			return fmt.Errorf("bulk put item %d (key %q): \"value\" must be a string", i, key)
		}
	}
	return nil
}

func kvValidateBulkDeleteBody(body []byte) ([]string, error) {
	var keys []string
	if err := json.Unmarshal(body, &keys); err != nil {
		return nil, errors.New("bulk delete file must be a JSON array of key name strings")
	}
	if len(keys) == 0 {
		return nil, errors.New("bulk delete file must contain at least one key")
	}
	if len(keys) > 10000 {
		return nil, fmt.Errorf("bulk delete supports at most 10000 keys (got %d)", len(keys))
	}
	for i, k := range keys {
		if strings.TrimSpace(k) == "" {
			return nil, fmt.Errorf("bulk delete key at index %d is empty", i)
		}
	}
	return keys, nil
}

func runKVRequest(cmd *cobra.Command, g *globalOpts, client *api.Client, req api.Request) error {
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

// runKVRawRequest is for endpoints that return a raw value body (key get),
// not a JSON envelope.
func runKVRawRequest(cmd *cobra.Command, g *globalOpts, client *api.Client, req api.Request) error {
	if g.DryRun {
		dump, err := client.Dump(req)
		if err != nil {
			return err
		}
		return g.renderValue(cmd, dump, output.JSON)
	}
	raw, err := client.DoRaw(cmd.Context(), req)
	if err != nil {
		return err
	}
	// Prefer JSON rendering when the stored value is JSON; otherwise emit the
	// bytes as a JSON string so --query/--output stay consistent.
	if json.Valid(raw.Body) {
		return g.renderResult(cmd, raw.Body, output.JSON)
	}
	return g.renderValue(cmd, string(raw.Body), output.JSON)
}
