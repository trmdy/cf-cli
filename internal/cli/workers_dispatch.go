package cli

// Workers dispatch porcelain manages Workers for Platforms namespaces and
// their scripts. Live bundle uploads deliberately use api.Client.DoStream so
// the local module is never collected in memory before it is sent.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/trmdy/cf-cli/internal/api"
	"github.com/trmdy/cf-cli/internal/config"
	"github.com/trmdy/cf-cli/internal/output"
)

type workersDispatchNamespace struct {
	NamespaceID    string `json:"namespace_id,omitempty"`
	NamespaceName  string `json:"namespace_name,omitempty"`
	ScriptCount    int    `json:"script_count,omitempty"`
	TrustedWorkers bool   `json:"trusted_workers,omitempty"`
	CreatedOn      string `json:"created_on,omitempty"`
	ModifiedOn     string `json:"modified_on,omitempty"`
}

type workersDispatchScript struct {
	CreatedOn         string `json:"created_on,omitempty"`
	DispatchNamespace string `json:"dispatch_namespace,omitempty"`
	ModifiedOn        string `json:"modified_on,omitempty"`
	Script            struct {
		ID                string `json:"id,omitempty"`
		CompatibilityDate string `json:"compatibility_date,omitempty"`
	} `json:"script,omitempty"`
}

func newWorkersDispatchCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dispatch",
		Short: "Manage Workers for Platforms dispatch namespaces",
	}
	cmd.AddCommand(
		newWorkersDispatchNamespaceCmd(g),
		newWorkersDispatchScriptCmd(g),
	)
	return cmd
}

func newWorkersDispatchNamespaceCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "namespace",
		Short: "Manage Workers for Platforms dispatch namespaces",
	}
	cmd.AddCommand(
		newWorkersDispatchNamespaceListCmd(g),
		newWorkersDispatchNamespaceGetCmd(g),
		newWorkersDispatchNamespaceCreateCmd(g),
		newWorkersDispatchNamespaceDeleteCmd(g),
	)
	return cmd
}

func newWorkersDispatchScriptCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "script",
		Short: "Manage scripts in a Workers for Platforms namespace",
	}
	cmd.AddCommand(
		newWorkersDispatchScriptListCmd(g),
		newWorkersDispatchScriptGetCmd(g),
		newWorkersDispatchScriptDeleteCmd(g),
		newWorkersDispatchScriptUploadCmd(g),
	)
	return cmd
}

func workersDispatchAccountID(cfg config.Resolved) (string, error) {
	accountID := strings.TrimSpace(cfg.AccountID)
	if accountID == "" {
		return "", errors.New("no account specified: pass --account-id, set CLOUDFLARE_ACCOUNT_ID, or configure a profile")
	}
	return accountID, nil
}

// workersDispatchClient validates the resolved global contract before making a
// client. Command-specific local input is always validated before this helper
// is called, so invalid flags cannot create a client or trigger a lookup.
func workersDispatchClient(g *globalOpts) (*api.Client, string, error) {
	cfg, err := g.resolve()
	if err != nil {
		return nil, "", err
	}
	accountID, err := workersDispatchAccountID(cfg)
	if err != nil {
		return nil, "", err
	}
	if !g.DryRun && cfg.Token == "" {
		return nil, "", errors.New("no API token found; run `cf auth login`, set CLOUDFLARE_API_TOKEN, or pass --token")
	}
	return api.New(g.BaseURL, cfg.Token, Version), accountID, nil
}

func workersDispatchNamespacesPath(accountID string) string {
	return "/accounts/" + url.PathEscape(accountID) + "/workers/dispatch/namespaces"
}

func workersDispatchNamespacePath(accountID, namespace string) string {
	return workersDispatchNamespacesPath(accountID) + "/" + url.PathEscape(namespace)
}

func workersDispatchScriptsPath(accountID, namespace string) string {
	return workersDispatchNamespacePath(accountID, namespace) + "/scripts"
}

func workersDispatchScriptPath(accountID, namespace, script string) string {
	return workersDispatchScriptsPath(accountID, namespace) + "/" + url.PathEscape(script)
}

func workersDispatchName(kind, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("%s cannot be empty", kind)
	}
	return value, nil
}

func newWorkersDispatchNamespaceListCmd(g *globalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List Workers for Platforms dispatch namespaces",
		Long:  "List Workers for Platforms dispatch namespaces in the configured account.\n\nExamples:\n\n  cf workers dispatch namespace list\n  cf workers dispatch namespace list --output json",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, accountID, err := workersDispatchClient(g)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: workersDispatchNamespacesPath(accountID)}
			if g.DryRun {
				return runWorkersDispatchRequest(cmd, g, client, req)
			}
			env, err := client.DoAutoPaginate(cmd.Context(), req)
			if err != nil {
				return err
			}
			format := g.format(output.Table)
			if g.Query != "" || format != output.Table {
				return g.renderResult(cmd, env.Result, output.JSON)
			}
			var namespaces []workersDispatchNamespace
			if err := json.Unmarshal(env.Result, &namespaces); err != nil {
				return output.RenderRaw(cmd.OutOrStdout(), output.JSON, env.Result)
			}
			rows := make([][]string, 0, len(namespaces))
			for _, namespace := range namespaces {
				rows = append(rows, []string{
					namespace.NamespaceName,
					namespace.NamespaceID,
					strconv.Itoa(namespace.ScriptCount),
					strconv.FormatBool(namespace.TrustedWorkers),
					namespace.ModifiedOn,
				})
			}
			return output.RenderTable(cmd.OutOrStdout(), []string{"NAME", "ID", "SCRIPTS", "TRUSTED", "MODIFIED"}, rows)
		},
	}
}

func newWorkersDispatchNamespaceGetCmd(g *globalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "get <namespace>",
		Short: "Show one Workers for Platforms dispatch namespace",
		Long:  "Show one Workers for Platforms dispatch namespace.\n\nExamples:\n\n  cf workers dispatch namespace get customer-workers",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			namespace, err := workersDispatchName("namespace name", args[0])
			if err != nil {
				return err
			}
			client, accountID, err := workersDispatchClient(g)
			if err != nil {
				return err
			}
			return runWorkersDispatchRequest(cmd, g, client, api.Request{Method: "GET", Path: workersDispatchNamespacePath(accountID, namespace)})
		},
	}
}

func newWorkersDispatchNamespaceCreateCmd(g *globalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "create <namespace>",
		Short: "Create a Workers for Platforms dispatch namespace",
		Long:  "Create a Workers for Platforms dispatch namespace.\n\nExamples:\n\n  cf workers dispatch namespace create customer-workers",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := buildWorkersDispatchNamespaceCreateBody(args[0])
			if err != nil {
				return err
			}
			client, accountID, err := workersDispatchClient(g)
			if err != nil {
				return err
			}
			return runWorkersDispatchRequest(cmd, g, client, api.Request{Method: "POST", Path: workersDispatchNamespacesPath(accountID), Body: body})
		},
	}
}

func buildWorkersDispatchNamespaceCreateBody(name string) ([]byte, error) {
	name, err := workersDispatchName("namespace name", name)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]string{"name": name})
}

func newWorkersDispatchNamespaceDeleteCmd(g *globalOpts) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <namespace>",
		Short: "Delete a Workers for Platforms dispatch namespace",
		Long:  "Delete a Workers for Platforms dispatch namespace.\n\nExamples:\n\n  cf workers dispatch namespace delete customer-workers --force",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			namespace, err := workersDispatchName("namespace name", args[0])
			if err != nil {
				return err
			}
			client, accountID, err := workersDispatchClient(g)
			if err != nil {
				return err
			}
			if !force && !g.DryRun {
				if !confirm(fmt.Sprintf("Delete Workers dispatch namespace %s?", namespace)) {
					return errors.New("aborted (pass --force to skip confirmation)")
				}
			}
			return runWorkersDispatchRequest(cmd, g, client, api.Request{Method: "DELETE", Path: workersDispatchNamespacePath(accountID, namespace)})
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	return cmd
}

func newWorkersDispatchScriptListCmd(g *globalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "list <namespace>",
		Short: "List scripts in a Workers for Platforms namespace",
		Long:  "List scripts in a Workers for Platforms namespace.\n\nExamples:\n\n  cf workers dispatch script list customer-workers\n  cf workers dispatch script list customer-workers --output json",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			namespace, err := workersDispatchName("namespace name", args[0])
			if err != nil {
				return err
			}
			client, accountID, err := workersDispatchClient(g)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: workersDispatchScriptsPath(accountID, namespace)}
			if g.DryRun {
				return runWorkersDispatchRequest(cmd, g, client, req)
			}
			env, err := client.DoAutoPaginate(cmd.Context(), req)
			if err != nil {
				return err
			}
			format := g.format(output.Table)
			if g.Query != "" || format != output.Table {
				return g.renderResult(cmd, env.Result, output.JSON)
			}
			var scripts []workersDispatchScript
			if err := json.Unmarshal(env.Result, &scripts); err != nil {
				return output.RenderRaw(cmd.OutOrStdout(), output.JSON, env.Result)
			}
			rows := make([][]string, 0, len(scripts))
			for _, script := range scripts {
				rows = append(rows, []string{script.Script.ID, script.Script.CompatibilityDate, script.ModifiedOn})
			}
			return output.RenderTable(cmd.OutOrStdout(), []string{"NAME", "COMPATIBILITY_DATE", "MODIFIED"}, rows)
		},
	}
}

func newWorkersDispatchScriptGetCmd(g *globalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "get <namespace> <script>",
		Short: "Show one Workers for Platforms script",
		Long:  "Show a script in a Workers for Platforms namespace.\n\nExamples:\n\n  cf workers dispatch script get customer-workers checkout",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			namespace, script, err := workersDispatchScriptArgs(args)
			if err != nil {
				return err
			}
			client, accountID, err := workersDispatchClient(g)
			if err != nil {
				return err
			}
			return runWorkersDispatchRequest(cmd, g, client, api.Request{Method: "GET", Path: workersDispatchScriptPath(accountID, namespace, script)})
		},
	}
}

func newWorkersDispatchScriptDeleteCmd(g *globalOpts) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <namespace> <script>",
		Short: "Delete a Workers for Platforms script",
		Long:  "Delete a script from a Workers for Platforms namespace.\n\nExamples:\n\n  cf workers dispatch script delete customer-workers checkout --force",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			namespace, script, err := workersDispatchScriptArgs(args)
			if err != nil {
				return err
			}
			client, accountID, err := workersDispatchClient(g)
			if err != nil {
				return err
			}
			if !force && !g.DryRun {
				if !confirm(fmt.Sprintf("Delete Workers dispatch script %s from %s?", script, namespace)) {
					return errors.New("aborted (pass --force to skip confirmation)")
				}
			}
			return runWorkersDispatchRequest(cmd, g, client, api.Request{Method: "DELETE", Path: workersDispatchScriptPath(accountID, namespace, script)})
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	return cmd
}

func workersDispatchScriptArgs(args []string) (namespace, script string, err error) {
	namespace, err = workersDispatchName("namespace name", args[0])
	if err != nil {
		return "", "", err
	}
	script, err = workersDispatchName("script name", args[1])
	if err != nil {
		return "", "", err
	}
	return namespace, script, nil
}

type workersDispatchUpload struct {
	FilePath        string
	Metadata        []byte
	BindingsInherit string
}

func newWorkersDispatchScriptUploadCmd(g *globalOpts) *cobra.Command {
	var file, metadata, bindingsInherit string
	cmd := &cobra.Command{
		Use:   "upload <namespace> <script>",
		Short: "Upload a module Worker to a dispatch namespace",
		Long: `Upload one module Worker file to a Workers for Platforms namespace.
The metadata object must contain a main_module value matching the basename of
--file. Live uploads stream the module without buffering it in memory.

Examples:

  cf workers dispatch script upload customer-workers checkout --file ./worker.js --metadata '{"main_module":"worker.js"}'
  cf workers dispatch script upload customer-workers checkout --file ./worker.js --metadata '{"main_module":"worker.js","compatibility_date":"2025-01-01"}' --bindings-inherit strict`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			namespace, script, err := workersDispatchScriptArgs(args)
			if err != nil {
				return err
			}
			upload, err := buildWorkersDispatchUpload(file, metadata, bindingsInherit, cmd.Flags().Changed("metadata"))
			if err != nil {
				return err
			}
			var dryRunBody []byte
			var dryRunContentType string
			if g.DryRun {
				dryRunBody, dryRunContentType, err = buildWorkersDispatchMultipartFixture(upload.FilePath, upload.Metadata)
				if err != nil {
					return err
				}
			}
			client, accountID, err := workersDispatchClient(g)
			if err != nil {
				return err
			}
			path := workersDispatchScriptPath(accountID, namespace, script)
			if g.DryRun {
				req := api.Request{Method: "PUT", Path: path, Query: workersDispatchUploadQuery(upload.BindingsInherit), Body: dryRunBody, ContentType: dryRunContentType}
				return runWorkersDispatchRequest(cmd, g, client, req)
			}
			body, contentType := newWorkersDispatchMultipartStream(upload.FilePath, upload.Metadata)
			return runWorkersDispatchUpload(cmd, g, client, api.Request{Method: "PUT", Path: path, Query: workersDispatchUploadQuery(upload.BindingsInherit), ContentType: contentType}, body)
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "local Worker module file to upload")
	cmd.Flags().StringVar(&metadata, "metadata", "", "JSON object containing Worker multipart metadata (must include main_module)")
	cmd.Flags().StringVar(&bindingsInherit, "bindings-inherit", "", "binding inheritance behavior (strict)")
	return cmd
}

// buildWorkersDispatchUpload validates every local upload input before a
// client is constructed. The API requires a metadata object whose main_module
// identifies an uploaded multipart part; this porcelain uploads exactly one
// part, named after --file's basename.
func buildWorkersDispatchUpload(file, metadata, bindingsInherit string, metadataSet bool) (workersDispatchUpload, error) {
	file = strings.TrimSpace(file)
	if file == "" {
		return workersDispatchUpload{}, errors.New("missing --file")
	}
	info, err := os.Stat(file)
	if err != nil {
		return workersDispatchUpload{}, fmt.Errorf("read --file: %w", err)
	}
	if info.IsDir() {
		return workersDispatchUpload{}, fmt.Errorf("--file %q is a directory", file)
	}
	probe, err := os.Open(file)
	if err != nil {
		return workersDispatchUpload{}, fmt.Errorf("open --file: %w", err)
	}
	if err := probe.Close(); err != nil {
		return workersDispatchUpload{}, fmt.Errorf("close --file: %w", err)
	}
	if !metadataSet {
		return workersDispatchUpload{}, errors.New("missing --metadata: provide a JSON object containing main_module")
	}
	canonicalMetadata, err := validateWorkersDispatchMetadata(metadata, filepath.Base(file))
	if err != nil {
		return workersDispatchUpload{}, err
	}
	bindingsInherit = strings.ToLower(strings.TrimSpace(bindingsInherit))
	if bindingsInherit != "" && bindingsInherit != "strict" {
		return workersDispatchUpload{}, errors.New("--bindings-inherit must be strict")
	}
	return workersDispatchUpload{FilePath: file, Metadata: canonicalMetadata, BindingsInherit: bindingsInherit}, nil
}

// validateWorkersDispatchMetadata accepts only a non-null JSON object and
// canonicalizes it before it is written into the multipart metadata part.
func validateWorkersDispatchMetadata(raw, expectedMainModule string) ([]byte, error) {
	var metadata map[string]any
	if err := json.Unmarshal([]byte(raw), &metadata); err != nil || metadata == nil {
		return nil, errors.New("--metadata must be a JSON object")
	}
	mainModule, ok := metadata["main_module"].(string)
	if !ok || strings.TrimSpace(mainModule) == "" {
		return nil, errors.New("--metadata.main_module must be a non-empty string")
	}
	if mainModule != expectedMainModule {
		return nil, fmt.Errorf("--metadata.main_module must equal the --file basename %q", expectedMainModule)
	}
	canonical, err := json.Marshal(metadata)
	if err != nil {
		return nil, fmt.Errorf("encode --metadata: %w", err)
	}
	return canonical, nil
}

func workersDispatchUploadQuery(bindingsInherit string) url.Values {
	if bindingsInherit == "" {
		return nil
	}
	return url.Values{"bindings_inherit": {bindingsInherit}}
}

// newWorkersDispatchMultipartStream constructs a multipart body around an
// io.Pipe. Its goroutine copies the module directly from disk into the HTTP
// request; no bundle-sized byte slice is created.
func newWorkersDispatchMultipartStream(filePath string, metadata []byte) (io.Reader, string) {
	reader, writer := io.Pipe()
	multipartWriter := multipart.NewWriter(writer)
	go func() {
		var writeErr error
		defer func() {
			if err := multipartWriter.Close(); writeErr == nil {
				writeErr = err
			}
			_ = writer.CloseWithError(writeErr)
		}()

		writeErr = writeWorkersDispatchMultipartParts(multipartWriter, filePath, metadata)
	}()
	return reader, multipartWriter.FormDataContentType()
}

// buildWorkersDispatchMultipartFixture creates the exact multipart request
// printed by --dry-run. Its fixed boundary makes the fixture reproducible;
// live uploads use newWorkersDispatchMultipartStream instead.
func buildWorkersDispatchMultipartFixture(filePath string, metadata []byte) ([]byte, string, error) {
	const boundary = "cf-cli-workers-dispatch-dry-run"
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.SetBoundary(boundary); err != nil {
		return nil, "", err
	}
	if err := writeWorkersDispatchMultipartParts(writer, filePath, metadata); err != nil {
		return nil, "", err
	}
	if err := writer.Close(); err != nil {
		return nil, "", err
	}
	return body.Bytes(), writer.FormDataContentType(), nil
}

func writeWorkersDispatchMultipartParts(writer *multipart.Writer, filePath string, metadata []byte) error {
	metadataHeader := textproto.MIMEHeader{}
	metadataHeader.Set("Content-Disposition", `form-data; name="metadata"`)
	metadataHeader.Set("Content-Type", "application/json")
	metadataPart, err := writer.CreatePart(metadataHeader)
	if err != nil {
		return err
	}
	if _, err := metadataPart.Write(metadata); err != nil {
		return err
	}

	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("open --file: %w", err)
	}
	defer file.Close()
	fileName := filepath.Base(filePath)
	contentDisposition := mime.FormatMediaType("form-data", map[string]string{"name": fileName, "filename": fileName})
	if contentDisposition == "" {
		return errors.New("build multipart module header")
	}
	fileHeader := textproto.MIMEHeader{}
	fileHeader.Set("Content-Disposition", contentDisposition)
	fileHeader.Set("Content-Type", "application/javascript+module")
	filePart, err := writer.CreatePart(fileHeader)
	if err != nil {
		return err
	}
	if _, err := io.Copy(filePart, file); err != nil {
		return err
	}
	return nil
}

func runWorkersDispatchRequest(cmd *cobra.Command, g *globalOpts, client *api.Client, req api.Request) error {
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

func runWorkersDispatchUpload(cmd *cobra.Command, g *globalOpts, client *api.Client, req api.Request, body io.Reader) error {
	response, err := client.DoStream(cmd.Context(), req, body)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}
	var env api.Envelope
	if err := json.Unmarshal(raw, &env); err != nil || env.Result == nil {
		return g.renderResult(cmd, raw, output.JSON)
	}
	if !env.Success {
		return &api.APIError{StatusCode: response.StatusCode, Errors: env.Errors, RawBody: string(raw)}
	}
	return g.renderResult(cmd, env.Result, output.JSON)
}
