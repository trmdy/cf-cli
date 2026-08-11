package cli

// Images porcelain: list/get/upload/delete images, variant CRUD, and usage.
// See docs/STYLE.md; internal/cli/dns.go is the shape exemplar.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/trmdy/cf-cli/internal/api"
	"github.com/trmdy/cf-cli/internal/output"
)

type imageRecord struct {
	ID                string         `json:"id,omitempty"`
	Filename          string         `json:"filename,omitempty"`
	Creator           string         `json:"creator,omitempty"`
	Meta              map[string]any `json:"meta,omitempty"`
	RequireSignedURLs bool           `json:"requireSignedURLs,omitempty"`
	Uploaded          string         `json:"uploaded,omitempty"`
	Variants          []string       `json:"variants,omitempty"`
}

type imageListResult struct {
	Images []imageRecord `json:"images"`
}

type imageVariantOptions struct {
	Fit      string `json:"fit"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	Metadata string `json:"metadata"`
}

type imageVariant struct {
	ID                     string               `json:"id,omitempty"`
	NeverRequireSignedURLs *bool                `json:"neverRequireSignedURLs,omitempty"`
	Options                *imageVariantOptions `json:"options,omitempty"`
}

type imageVariantListResult struct {
	Variants map[string]imageVariant `json:"variants"`
}

type imagesUsageResult struct {
	Count struct {
		Current float64 `json:"current"`
		Allowed float64 `json:"allowed"`
	} `json:"count"`
}

func newImagesCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "images",
		Short: "Manage Cloudflare Images",
	}
	cmd.AddCommand(
		newImagesListCmd(g),
		newImagesGetCmd(g),
		newImagesUploadCmd(g),
		newImagesDeleteCmd(g),
		newImagesVariantCmd(g),
		newImagesUsageCmd(g),
	)
	return cmd
}

func imagesBasePath(accountID string) string {
	return "/accounts/" + accountID + "/images/v1"
}

func imagesPath(accountID string) string {
	return imagesBasePath(accountID)
}

func imagePath(accountID, imageID string) string {
	return imagesBasePath(accountID) + "/" + url.PathEscape(imageID)
}

func imagesVariantsPath(accountID string) string {
	return imagesBasePath(accountID) + "/variants"
}

func imagesVariantPath(accountID, variantID string) string {
	return imagesVariantsPath(accountID) + "/" + url.PathEscape(variantID)
}

func imagesStatsPath(accountID string) string {
	return imagesBasePath(accountID) + "/stats"
}

func requireAccountID(accountID string) (string, error) {
	if accountID == "" {
		return "", errors.New("missing account ID: pass --account-id, set CLOUDFLARE_ACCOUNT_ID, or configure a profile")
	}
	return accountID, nil
}

func newImagesListCmd(g *globalOpts) *cobra.Command {
	var creator string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List images in the account",
		Long: `List images stored in Cloudflare Images.

Examples:

  cf images list
  cf images list --creator user-123
  cf images list --output json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := requireAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			if g.DryRun {
				q := url.Values{}
				q.Set("per_page", "100")
				q.Set("page", "1")
				if creator != "" {
					q.Set("creator", creator)
				}
				req := api.Request{Method: "GET", Path: imagesPath(accountID), Query: q}
				return runImagesRequest(cmd, g, client, req)
			}
			images, raw, err := listAllImages(cmd.Context(), client, accountID, creator)
			if err != nil {
				return err
			}
			format := g.format(output.Table)
			if g.Query != "" || format != output.Table {
				return g.renderResult(cmd, raw, output.JSON)
			}
			rows := make([][]string, 0, len(images))
			for _, img := range images {
				signed := strconv.FormatBool(img.RequireSignedURLs)
				rows = append(rows, []string{
					img.ID,
					output.Cell(img.Filename),
					img.Uploaded,
					signed,
					strconv.Itoa(len(img.Variants)),
				})
			}
			return output.RenderTable(cmd.OutOrStdout(), []string{"ID", "FILENAME", "UPLOADED", "SIGNED", "VARIANTS"}, rows)
		},
	}
	cmd.Flags().StringVar(&creator, "creator", "", "filter by creator ID (empty string returns images with no creator)")
	return cmd
}

// listAllImages pages through GET /images/v1 and returns the merged image list
// plus a JSON array of those images (for --query/--output).
func listAllImages(ctx context.Context, client *api.Client, accountID, creator string) ([]imageRecord, []byte, error) {
	const perPage = 100
	var all []imageRecord
	for page := 1; page <= 1000; page++ {
		q := url.Values{}
		q.Set("per_page", strconv.Itoa(perPage))
		q.Set("page", strconv.Itoa(page))
		if creator != "" {
			q.Set("creator", creator)
		}
		env, err := client.Do(ctx, api.Request{Method: "GET", Path: imagesPath(accountID), Query: q})
		if err != nil {
			return nil, nil, err
		}
		var res imageListResult
		if err := json.Unmarshal(env.Result, &res); err != nil {
			// Unexpected shape: return the raw page result as a single page.
			if page == 1 {
				return nil, env.Result, nil
			}
			return nil, nil, fmt.Errorf("list images page %d: unexpected response", page)
		}
		all = append(all, res.Images...)
		if len(res.Images) < perPage {
			break
		}
		if env.ResultInfo != nil && env.ResultInfo.TotalPages > 0 && page >= env.ResultInfo.TotalPages {
			break
		}
	}
	raw, err := json.Marshal(all)
	if err != nil {
		return nil, nil, err
	}
	return all, raw, nil
}

func newImagesGetCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <image-id>",
		Short: "Show one image",
		Long: `Show details for one image.

Examples:

  cf images get 2cdc28f0-017a-49c4-9ed7-87056c83901`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := requireAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: imagePath(accountID, args[0])}
			return runImagesRequest(cmd, g, client, req)
		},
	}
	return cmd
}

func newImagesUploadCmd(g *globalOpts) *cobra.Command {
	var file, sourceURL, id, metadata, creator string
	var requireSignedURLs bool
	cmd := &cobra.Command{
		Use:   "upload",
		Short: "Upload an image from a file or URL",
		Long: `Upload an image to Cloudflare Images via a local file or a remote URL.
Exactly one of --file or --url is required. The Images API uses
multipart/form-data for uploads.

Examples:

  cf images upload --file ./logo.png
  cf images upload --url https://example.com/logo.png --id brand-logo
  cf images upload --file ./hero.jpg --metadata '{"album":"homepage"}' --require-signed-urls`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			fields, fileField, filePath, err := buildImagesUploadForm(file, sourceURL, id, metadata, creator, requireSignedURLs, cmd.Flags().Changed("require-signed-urls"))
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := requireAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			path := imagesPath(accountID)
			if g.DryRun {
				return g.renderValue(cmd, imagesUploadDryRunDump(client, path, fields, fileField, filePath), output.JSON)
			}
			env, err := doImagesMultipart(cmd.Context(), client, path, fields, fileField, filePath)
			if err != nil {
				return err
			}
			return g.renderResult(cmd, env.Result, output.JSON)
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "local image file to upload")
	cmd.Flags().StringVar(&sourceURL, "url", "", "remote image URL for Cloudflare to fetch")
	cmd.Flags().StringVar(&id, "id", "", "optional custom image ID")
	cmd.Flags().StringVar(&metadata, "metadata", "", "JSON object of user metadata")
	cmd.Flags().StringVar(&creator, "creator", "", "internal creator ID")
	cmd.Flags().BoolVar(&requireSignedURLs, "require-signed-urls", false, "require signed URLs to access the image")
	return cmd
}

// buildImagesUploadForm validates upload flags and returns multipart fields
// plus optional file field/path. metadata must be a JSON object when set.
func buildImagesUploadForm(file, sourceURL, id, metadata, creator string, requireSignedURLs, requireSignedSet bool) (fields map[string]string, fileField, filePath string, err error) {
	file = strings.TrimSpace(file)
	sourceURL = strings.TrimSpace(sourceURL)
	if file == "" && sourceURL == "" {
		return nil, "", "", errors.New("specify an upload source: --file or --url")
	}
	if file != "" && sourceURL != "" {
		return nil, "", "", errors.New("specify exactly one of --file or --url")
	}
	fields = map[string]string{}
	if sourceURL != "" {
		fields["url"] = sourceURL
	}
	if file != "" {
		if st, statErr := os.Stat(file); statErr != nil {
			return nil, "", "", fmt.Errorf("read --file: %w", statErr)
		} else if st.IsDir() {
			return nil, "", "", fmt.Errorf("--file %q is a directory", file)
		}
		fileField = "file"
		filePath = file
	}
	if id != "" {
		fields["id"] = id
	}
	if creator != "" {
		fields["creator"] = creator
	}
	if metadata != "" {
		var obj map[string]any
		if err := json.Unmarshal([]byte(metadata), &obj); err != nil {
			return nil, "", "", errors.New("--metadata must be a JSON object")
		}
		// Cloudflare expects metadata as a JSON string form field.
		compact, err := json.Marshal(obj)
		if err != nil {
			return nil, "", "", err
		}
		fields["metadata"] = string(compact)
	}
	if requireSignedSet {
		fields["requireSignedURLs"] = strconv.FormatBool(requireSignedURLs)
	}
	return fields, fileField, filePath, nil
}

func imagesUploadDryRunDump(client *api.Client, path string, fields map[string]string, fileField, filePath string) map[string]any {
	u := strings.TrimRight(client.BaseURL, "/") + path
	body := map[string]any{}
	for k, v := range fields {
		body[k] = v
	}
	if fileField != "" && filePath != "" {
		body[fileField] = map[string]any{
			"filename": filepath.Base(filePath),
			"path":     filePath,
		}
	}
	headers := map[string]string{
		"Accept":       "application/json",
		"Content-Type": "multipart/form-data",
		"User-Agent":   client.UserAgent,
	}
	if client.Token != "" {
		headers["Authorization"] = "Bearer ********"
	}
	return map[string]any{
		"method":  "POST",
		"url":     u,
		"headers": headers,
		"body":    body,
	}
}

// doImagesMultipart POSTs multipart/form-data. The shared api.Client always
// sets Content-Type to application/json, so upload must use the client's
// HTTP transport directly while preserving auth and base URL.
func doImagesMultipart(ctx context.Context, client *api.Client, path string, fields map[string]string, fileField, filePath string) (*api.Envelope, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if err := w.WriteField(k, fields[k]); err != nil {
			return nil, err
		}
	}
	if fileField != "" && filePath != "" {
		f, err := os.Open(filePath)
		if err != nil {
			return nil, fmt.Errorf("open --file: %w", err)
		}
		defer f.Close()
		part, err := w.CreateFormFile(fileField, filepath.Base(filePath))
		if err != nil {
			return nil, err
		}
		if _, err := io.Copy(part, f); err != nil {
			return nil, err
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}

	u := strings.TrimRight(client.BaseURL, "/") + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, &buf)
	if err != nil {
		return nil, err
	}
	if client.Token != "" {
		req.Header.Set("Authorization", "Bearer "+client.Token)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("User-Agent", client.UserAgent)
	req.Header.Set("Accept", "application/json")

	httpClient := client.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 100<<20))
	if err != nil {
		return nil, err
	}
	env := &api.Envelope{}
	if len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, env); err != nil {
			// Non-envelope body: treat as raw result on 2xx.
			if resp.StatusCode < 400 {
				env.Success = true
				env.Result = json.RawMessage(data)
				return env, nil
			}
			return nil, &api.APIError{StatusCode: resp.StatusCode, RawBody: string(data)}
		}
	} else {
		env.Success = resp.StatusCode < 400
	}
	if resp.StatusCode >= 400 || !env.Success {
		return env, &api.APIError{StatusCode: resp.StatusCode, Errors: env.Errors, RawBody: string(data)}
	}
	return env, nil
}

func newImagesDeleteCmd(g *globalOpts) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <image-id>",
		Short: "Delete an image",
		Long: `Delete an image from Cloudflare Images.

Examples:

  cf images delete 2cdc28f0-017a-49c4-9ed7-87056c83901 --force`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := requireAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			if !force && !g.DryRun {
				if !confirm(fmt.Sprintf("Delete image %s?", args[0])) {
					return errors.New("aborted (pass --force to skip confirmation)")
				}
			}
			req := api.Request{Method: "DELETE", Path: imagePath(accountID, args[0])}
			return runImagesRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	return cmd
}

func newImagesVariantCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "variant",
		Short: "Manage image variants",
	}
	cmd.AddCommand(
		newImagesVariantListCmd(g),
		newImagesVariantGetCmd(g),
		newImagesVariantCreateCmd(g),
		newImagesVariantUpdateCmd(g),
		newImagesVariantDeleteCmd(g),
	)
	return cmd
}

func newImagesVariantListCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List image variants",
		Long: `List configured Cloudflare Images variants.

Examples:

  cf images variant list
  cf images variant list --output json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := requireAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: imagesVariantsPath(accountID)}
			if g.DryRun {
				return runImagesRequest(cmd, g, client, req)
			}
			env, err := client.Do(cmd.Context(), req)
			if err != nil {
				return err
			}
			format := g.format(output.Table)
			if g.Query != "" || format != output.Table {
				return g.renderResult(cmd, env.Result, output.JSON)
			}
			variants, err := parseVariantList(env.Result)
			if err != nil {
				return output.RenderRaw(cmd.OutOrStdout(), output.JSON, env.Result)
			}
			ids := make([]string, 0, len(variants))
			for id := range variants {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			rows := make([][]string, 0, len(ids))
			for _, id := range ids {
				v := variants[id]
				fit, width, height, meta, never := variantTableCells(v)
				rows = append(rows, []string{id, width, height, fit, meta, never})
			}
			return output.RenderTable(cmd.OutOrStdout(), []string{"ID", "WIDTH", "HEIGHT", "FIT", "METADATA", "NEVER_SIGNED"}, rows)
		},
	}
	return cmd
}

func parseVariantList(raw []byte) (map[string]imageVariant, error) {
	var res imageVariantListResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, err
	}
	if res.Variants == nil {
		return map[string]imageVariant{}, nil
	}
	return res.Variants, nil
}

func variantTableCells(v imageVariant) (fit, width, height, meta, never string) {
	if v.Options != nil {
		fit = v.Options.Fit
		width = strconv.Itoa(v.Options.Width)
		height = strconv.Itoa(v.Options.Height)
		meta = v.Options.Metadata
	}
	if v.NeverRequireSignedURLs != nil {
		never = strconv.FormatBool(*v.NeverRequireSignedURLs)
	}
	return fit, width, height, meta, never
}

func newImagesVariantGetCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <variant-id>",
		Short: "Show one image variant",
		Long: `Show details for one image variant.

Examples:

  cf images variant get hero`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := requireAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: imagesVariantPath(accountID, args[0])}
			return runImagesRequest(cmd, g, client, req)
		},
	}
	return cmd
}

func newImagesVariantCreateCmd(g *globalOpts) *cobra.Command {
	var fit, metadata string
	var width, height int
	var neverRequireSignedURLs bool
	cmd := &cobra.Command{
		Use:   "create <variant-id>",
		Short: "Create an image variant",
		Long: `Create a Cloudflare Images variant for resizing.

Examples:

  cf images variant create hero --width 1366 --height 768 --fit scale-down --metadata none
  cf images variant create thumb --width 200 --height 200 --fit cover --metadata none --never-require-signed-urls`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := buildImagesVariantCreateBody(args[0], width, height, fit, metadata, neverRequireSignedURLs, cmd.Flags().Changed("never-require-signed-urls"))
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := requireAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			req := api.Request{Method: "POST", Path: imagesVariantsPath(accountID), Body: body}
			return runImagesRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().IntVar(&width, "width", 0, "maximum width in pixels")
	cmd.Flags().IntVar(&height, "height", 0, "maximum height in pixels")
	cmd.Flags().StringVar(&fit, "fit", "", "fit mode: scale-down, contain, cover, crop, or pad")
	cmd.Flags().StringVar(&metadata, "metadata", "", "EXIF metadata mode: keep, copyright, or none")
	cmd.Flags().BoolVar(&neverRequireSignedURLs, "never-require-signed-urls", false, "allow unsigned access to this variant")
	_ = cmd.MarkFlagRequired("width")
	_ = cmd.MarkFlagRequired("height")
	_ = cmd.MarkFlagRequired("fit")
	_ = cmd.MarkFlagRequired("metadata")
	return cmd
}

func buildImagesVariantCreateBody(id string, width, height int, fit, metadata string, never bool, neverSet bool) ([]byte, error) {
	opts, err := buildImagesVariantOptions(width, height, fit, metadata)
	if err != nil {
		return nil, err
	}
	body := map[string]any{
		"id":      id,
		"options": opts,
	}
	if neverSet {
		body["neverRequireSignedURLs"] = never
	}
	return json.Marshal(body)
}

func newImagesVariantUpdateCmd(g *globalOpts) *cobra.Command {
	var fit, metadata string
	var width, height int
	var neverRequireSignedURLs bool
	cmd := &cobra.Command{
		Use:   "update <variant-id>",
		Short: "Update an image variant",
		Long: `Update a Cloudflare Images variant. The API requires a full options object,
so --width, --height, --fit, and --metadata are all required.

Examples:

  cf images variant update hero --width 1600 --height 900 --fit cover --metadata none
  cf images variant update hero --width 1600 --height 900 --fit cover --metadata none --never-require-signed-urls`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := buildImagesVariantUpdateBody(width, height, fit, metadata, neverRequireSignedURLs, cmd.Flags().Changed("never-require-signed-urls"))
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := requireAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			req := api.Request{Method: "PATCH", Path: imagesVariantPath(accountID, args[0]), Body: body}
			return runImagesRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().IntVar(&width, "width", 0, "maximum width in pixels")
	cmd.Flags().IntVar(&height, "height", 0, "maximum height in pixels")
	cmd.Flags().StringVar(&fit, "fit", "", "fit mode: scale-down, contain, cover, crop, or pad")
	cmd.Flags().StringVar(&metadata, "metadata", "", "EXIF metadata mode: keep, copyright, or none")
	cmd.Flags().BoolVar(&neverRequireSignedURLs, "never-require-signed-urls", false, "allow unsigned access to this variant")
	_ = cmd.MarkFlagRequired("width")
	_ = cmd.MarkFlagRequired("height")
	_ = cmd.MarkFlagRequired("fit")
	_ = cmd.MarkFlagRequired("metadata")
	return cmd
}

func buildImagesVariantUpdateBody(width, height int, fit, metadata string, never bool, neverSet bool) ([]byte, error) {
	opts, err := buildImagesVariantOptions(width, height, fit, metadata)
	if err != nil {
		return nil, err
	}
	body := map[string]any{"options": opts}
	if neverSet {
		body["neverRequireSignedURLs"] = never
	}
	return json.Marshal(body)
}

func buildImagesVariantOptions(width, height int, fit, metadata string) (map[string]any, error) {
	if width < 1 {
		return nil, errors.New("--width must be at least 1")
	}
	if height < 1 {
		return nil, errors.New("--height must be at least 1")
	}
	fit = strings.ToLower(strings.TrimSpace(fit))
	switch fit {
	case "scale-down", "contain", "cover", "crop", "pad":
	default:
		return nil, errors.New("--fit must be one of: scale-down, contain, cover, crop, pad")
	}
	metadata = strings.ToLower(strings.TrimSpace(metadata))
	switch metadata {
	case "keep", "copyright", "none":
	default:
		return nil, errors.New("--metadata must be one of: keep, copyright, none")
	}
	return map[string]any{
		"width":    width,
		"height":   height,
		"fit":      fit,
		"metadata": metadata,
	}, nil
}

func newImagesVariantDeleteCmd(g *globalOpts) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <variant-id>",
		Short: "Delete an image variant",
		Long: `Delete a Cloudflare Images variant.

Examples:

  cf images variant delete hero --force`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := requireAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			if !force && !g.DryRun {
				if !confirm(fmt.Sprintf("Delete image variant %s?", args[0])) {
					return errors.New("aborted (pass --force to skip confirmation)")
				}
			}
			req := api.Request{Method: "DELETE", Path: imagesVariantPath(accountID, args[0])}
			return runImagesRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	return cmd
}

func newImagesUsageCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "usage",
		Short: "Show Images usage statistics",
		Long: `Show current and allowed Cloudflare Images usage counts.

Examples:

  cf images usage
  cf images usage --output table`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := requireAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: imagesStatsPath(accountID)}
			if g.DryRun {
				return runImagesRequest(cmd, g, client, req)
			}
			env, err := client.Do(cmd.Context(), req)
			if err != nil {
				return err
			}
			format := g.format(output.JSON)
			if g.Query != "" || format != output.Table {
				return g.renderResult(cmd, env.Result, output.JSON)
			}
			var stats imagesUsageResult
			if err := json.Unmarshal(env.Result, &stats); err != nil {
				return output.RenderRaw(cmd.OutOrStdout(), output.JSON, env.Result)
			}
			rows := [][]string{{
				formatImagesCount(stats.Count.Current),
				formatImagesCount(stats.Count.Allowed),
			}}
			return output.RenderTable(cmd.OutOrStdout(), []string{"CURRENT", "ALLOWED"}, rows)
		},
	}
	return cmd
}

func formatImagesCount(n float64) string {
	if n == float64(int64(n)) {
		return strconv.FormatInt(int64(n), 10)
	}
	return strconv.FormatFloat(n, 'f', -1, 64)
}

func runImagesRequest(cmd *cobra.Command, g *globalOpts, client *api.Client, req api.Request) error {
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
