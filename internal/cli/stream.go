package cli

// Stream porcelain: video list/get/delete, direct-upload URL creation, and
// signed playback tokens. TUS uploads are out of scope. See docs/STYLE.md;
// internal/cli/dns.go is the shape exemplar.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/trmdy/cf-cli/internal/api"
	"github.com/trmdy/cf-cli/internal/config"
	"github.com/trmdy/cf-cli/internal/output"
)

// streamVideo is the subset of Stream video fields used for table rendering.
type streamVideo struct {
	UID     string         `json:"uid,omitempty"`
	Meta    map[string]any `json:"meta,omitempty"`
	Creator string         `json:"creator,omitempty"`
	// Duration is seconds; API uses -1 when unknown.
	Duration float64 `json:"duration,omitempty"`
	Status   struct {
		State string `json:"state,omitempty"`
	} `json:"status,omitempty"`
	ReadyToStream     bool   `json:"readyToStream,omitempty"`
	Created           string `json:"created,omitempty"`
	Uploaded          string `json:"uploaded,omitempty"`
	RequireSignedURLs bool   `json:"requireSignedURLs,omitempty"`
}

func newStreamCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stream",
		Short: "Manage Cloudflare Stream videos",
	}
	cmd.AddCommand(
		newStreamListCmd(g),
		newStreamGetCmd(g),
		newStreamDeleteCmd(g),
		newStreamUploadCmd(g),
		newStreamTokenCmd(g),
	)
	return cmd
}

// requireStreamAccountID is product-scoped so it does not collide with other
// porcelain packages that need the same check under a different name.
func requireStreamAccountID(cfg config.Resolved) (string, error) {
	if cfg.AccountID == "" {
		return "", errors.New("missing account ID: pass --account-id, set CLOUDFLARE_ACCOUNT_ID, or configure a profile")
	}
	return cfg.AccountID, nil
}

func streamVideosPath(accountID string) string {
	return "/accounts/" + accountID + "/stream"
}

func streamVideoPath(accountID, videoID string) string {
	return streamVideosPath(accountID) + "/" + url.PathEscape(videoID)
}

func streamDirectUploadPath(accountID string) string {
	return streamVideosPath(accountID) + "/direct_upload"
}

func streamTokenPath(accountID, videoID string) string {
	return streamVideoPath(accountID, videoID) + "/token"
}

// streamListMax is the Stream list API hard cap per request.
const streamListMax = 1000

// streamMaxDurationSecondsMax is the documented API maximum for maxDurationSeconds.
const streamMaxDurationSecondsMax = 36000

// streamTokenMaxLifetime is the Stream signed-token maximum lifetime.
const streamTokenMaxLifetime = 24 * time.Hour

func newStreamListCmd(g *globalOpts) *cobra.Command {
	var search, status, vtype, creator, name, after, before string
	var limit int
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List Stream videos",
		Long: `List videos in the account Stream library.

Stream returns at most 1000 videos per request and does not expose standard
page/cursor result_info, so this command issues a single request (no silent
auto-pagination). For libraries larger than 1000 videos, window the results
with --after / --before (RFC 3339 creation-time bounds) and --limit.

Examples:

  cf stream list
  cf stream list --status ready
  cf stream list --search promo --type vod
  cf stream list --name video12345.mp4
  cf stream list --after 2026-01-01T00:00:00Z --before 2026-02-01T00:00:00Z --limit 100`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := requireStreamAccountID(cfg)
			if err != nil {
				return err
			}
			q, err := buildStreamListQuery(streamListOpts{
				Search:      search,
				Status:      status,
				Type:        vtype,
				Creator:     creator,
				Name:        name,
				After:       after,
				Before:      before,
				Limit:       limit,
				LimitSet:    cmd.Flags().Changed("limit"),
			})
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: streamVideosPath(accountID), Query: q}
			if g.DryRun {
				dump, err := client.Dump(req)
				if err != nil {
					return err
				}
				return g.renderValue(cmd, dump, output.JSON)
			}
			// Single request: Stream list has no result_info cursors for DoAutoPaginate.
			env, err := client.Do(cmd.Context(), req)
			if err != nil {
				return err
			}
			format := g.format(output.Table)
			if g.Query != "" || format != output.Table {
				return g.renderResult(cmd, env.Result, output.JSON)
			}
			var videos []streamVideo
			if err := json.Unmarshal(env.Result, &videos); err != nil {
				return output.RenderRaw(cmd.OutOrStdout(), output.JSON, env.Result)
			}
			rows := make([][]string, 0, len(videos))
			for _, v := range videos {
				rows = append(rows, []string{
					v.UID,
					output.Cell(streamVideoName(v)),
					streamVideoState(v),
					streamFormatDuration(v.Duration),
					strconv.FormatBool(v.ReadyToStream),
					streamCreatedAt(v),
				})
			}
			return output.RenderTable(cmd.OutOrStdout(),
				[]string{"UID", "NAME", "STATUS", "DURATION", "READY", "CREATED"},
				rows,
			)
		},
	}
	cmd.Flags().StringVar(&search, "search", "", "partial match on meta.name")
	cmd.Flags().StringVar(&status, "status", "", "filter by status (pendingupload, downloading, queued, inprogress, ready, error, live-inprogress)")
	cmd.Flags().StringVar(&vtype, "type", "", "filter by type: vod or live")
	cmd.Flags().StringVar(&creator, "creator", "", "filter by creator ID")
	cmd.Flags().StringVar(&name, "name", "", "exact match on meta.name")
	cmd.Flags().StringVar(&after, "after", "", "only videos created after this RFC 3339 timestamp")
	cmd.Flags().StringVar(&before, "before", "", "only videos created before this RFC 3339 timestamp")
	cmd.Flags().IntVar(&limit, "limit", streamListMax, "maximum videos to return (1-1000; Stream API cap)")
	return cmd
}

type streamListOpts struct {
	Search   string
	Status   string
	Type     string
	Creator  string
	Name     string
	After    string
	Before   string
	Limit    int
	LimitSet bool
}

// buildStreamListQuery validates list flags and builds the Stream list query.
// Always sets limit so callers see the explicit per-request cap.
func buildStreamListQuery(o streamListOpts) (url.Values, error) {
	limit := o.Limit
	if !o.LimitSet && limit == 0 {
		limit = streamListMax
	}
	if limit < 1 || limit > streamListMax {
		return nil, fmt.Errorf("--limit must be between 1 and %d (Stream API maximum per request)", streamListMax)
	}
	if o.After != "" {
		if _, err := time.Parse(time.RFC3339, o.After); err != nil {
			return nil, fmt.Errorf("--after must be RFC 3339 (e.g. 2026-01-01T00:00:00Z): %w", err)
		}
	}
	if o.Before != "" {
		if _, err := time.Parse(time.RFC3339, o.Before); err != nil {
			return nil, fmt.Errorf("--before must be RFC 3339 (e.g. 2026-02-01T00:00:00Z): %w", err)
		}
	}
	if o.After != "" && o.Before != "" {
		a, _ := time.Parse(time.RFC3339, o.After)
		b, _ := time.Parse(time.RFC3339, o.Before)
		if !a.Before(b) {
			return nil, errors.New("--after must be earlier than --before")
		}
	}

	q := url.Values{}
	if o.Search != "" {
		q.Set("search", o.Search)
	}
	if o.Status != "" {
		q.Set("status", o.Status)
	}
	if o.Type != "" {
		q.Set("type", o.Type)
	}
	if o.Creator != "" {
		q.Set("creator", o.Creator)
	}
	if o.Name != "" {
		q.Set("video_name", o.Name)
	}
	if o.After != "" {
		q.Set("after", o.After)
	}
	if o.Before != "" {
		q.Set("before", o.Before)
	}
	q.Set("limit", strconv.Itoa(limit))
	return q, nil
}

func streamVideoName(v streamVideo) string {
	if v.Meta != nil {
		if n, ok := v.Meta["name"].(string); ok {
			return n
		}
	}
	return ""
}

func streamVideoState(v streamVideo) string {
	if v.Status.State != "" {
		return v.Status.State
	}
	return ""
}

func streamCreatedAt(v streamVideo) string {
	if v.Created != "" {
		return v.Created
	}
	return v.Uploaded
}

func streamFormatDuration(d float64) string {
	if d < 0 {
		return ""
	}
	if d == 0 {
		return "0s"
	}
	// Prefer compact whole-second display when fractional part is noise.
	if d == float64(int64(d)) {
		return fmt.Sprintf("%ds", int64(d))
	}
	return fmt.Sprintf("%.1fs", d)
}

func newStreamGetCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <video-id>",
		Short: "Show one Stream video",
		Long: `Show details for a single Stream video.

Examples:

  cf stream get ea95132c15732412d22c1476fa83f27a`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := requireStreamAccountID(cfg)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: streamVideoPath(accountID, args[0])}
			return runStreamRequest(cmd, g, client, req)
		},
	}
	return cmd
}

func newStreamDeleteCmd(g *globalOpts) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <video-id>",
		Short: "Delete a Stream video",
		Long: `Delete a Stream video permanently.

Examples:

  cf stream delete ea95132c15732412d22c1476fa83f27a --force`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := requireStreamAccountID(cfg)
			if err != nil {
				return err
			}
			if !force && !g.DryRun {
				if !confirm(fmt.Sprintf("Delete Stream video %s?", args[0])) {
					return errors.New("aborted (pass --force to skip confirmation)")
				}
			}
			req := api.Request{Method: "DELETE", Path: streamVideoPath(accountID, args[0])}
			return runStreamRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	return cmd
}

func newStreamUploadCmd(g *globalOpts) *cobra.Command {
	var maxDurationSeconds int
	var expiry, creator, name, scheduledDeletion, watermarkUID string
	var requireSignedURLs bool
	var allowedOrigins []string
	var thumbnailTimestampPct float64
	cmd := &cobra.Command{
		Use:   "upload",
		Short: "Create a direct upload URL for Stream",
		Long: `Create a one-time direct upload URL so a client can POST a video without an API token.

This creates the upload URL only (basic POST direct creator upload). TUS / resumable
uploads are not supported by this command.

Examples:

  cf stream upload --max-duration-seconds 3600
  cf stream upload --max-duration-seconds 600 --name promo.mp4 --require-signed-urls
  cf stream upload --max-duration-seconds 300 --expiry 2026-12-01T00:00:00Z --creator user-42`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := buildStreamDirectUploadBody(streamDirectUploadOpts{
				MaxDurationSeconds:    maxDurationSeconds,
				Expiry:                expiry,
				Creator:               creator,
				Name:                  name,
				ScheduledDeletion:     scheduledDeletion,
				WatermarkUID:          watermarkUID,
				RequireSignedURLs:     requireSignedURLs,
				RequireSignedURLsSet:  cmd.Flags().Changed("require-signed-urls"),
				AllowedOrigins:        allowedOrigins,
				ThumbnailTimestampPct: thumbnailTimestampPct,
				ThumbnailPctSet:       cmd.Flags().Changed("thumbnail-timestamp-pct"),
			})
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := requireStreamAccountID(cfg)
			if err != nil {
				return err
			}
			req := api.Request{Method: "POST", Path: streamDirectUploadPath(accountID), Body: body}
			return runStreamRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().IntVar(&maxDurationSeconds, "max-duration-seconds", 0, "maximum accepted video duration in seconds (required; 1-36000, or -1 if unknown)")
	cmd.Flags().StringVar(&expiry, "expiry", "", "RFC 3339 time after which the upload URL rejects uploads")
	cmd.Flags().StringVar(&creator, "creator", "", "user-defined creator ID stored on the video")
	cmd.Flags().StringVar(&name, "name", "", "store as meta.name on the resulting video")
	cmd.Flags().StringArrayVar(&allowedOrigins, "allowed-origin", nil, "origin allowed to display the video (repeatable)")
	cmd.Flags().BoolVar(&requireSignedURLs, "require-signed-urls", false, "require signed URLs to view the uploaded video")
	cmd.Flags().StringVar(&scheduledDeletion, "scheduled-deletion", "", "RFC 3339 time when Stream should delete the video")
	cmd.Flags().Float64Var(&thumbnailTimestampPct, "thumbnail-timestamp-pct", 0, "thumbnail position as a fraction of duration (0.0-1.0)")
	cmd.Flags().StringVar(&watermarkUID, "watermark-uid", "", "watermark profile UID to apply")
	_ = cmd.MarkFlagRequired("max-duration-seconds")
	return cmd
}

type streamDirectUploadOpts struct {
	MaxDurationSeconds    int
	Expiry                string
	Creator               string
	Name                  string
	ScheduledDeletion     string
	WatermarkUID          string
	RequireSignedURLs     bool
	RequireSignedURLsSet  bool
	AllowedOrigins        []string
	ThumbnailTimestampPct float64
	ThumbnailPctSet       bool
}

// buildStreamDirectUploadBody validates flags and builds the JSON body for
// POST /accounts/{account_id}/stream/direct_upload.
func buildStreamDirectUploadBody(o streamDirectUploadOpts) ([]byte, error) {
	// 0 is invalid for storage reservation; only -1 (unknown) or 1..36000 allowed.
	if o.MaxDurationSeconds != -1 && (o.MaxDurationSeconds < 1 || o.MaxDurationSeconds > streamMaxDurationSecondsMax) {
		return nil, fmt.Errorf("--max-duration-seconds must be -1 or between 1 and %d", streamMaxDurationSecondsMax)
	}
	if o.Expiry != "" {
		if _, err := time.Parse(time.RFC3339, o.Expiry); err != nil {
			return nil, fmt.Errorf("--expiry must be RFC 3339 (e.g. 2026-12-01T00:00:00Z): %w", err)
		}
	}
	if o.ScheduledDeletion != "" {
		if _, err := time.Parse(time.RFC3339, o.ScheduledDeletion); err != nil {
			return nil, fmt.Errorf("--scheduled-deletion must be RFC 3339: %w", err)
		}
	}
	if o.ThumbnailPctSet && (o.ThumbnailTimestampPct < 0 || o.ThumbnailTimestampPct > 1) {
		return nil, errors.New("--thumbnail-timestamp-pct must be between 0.0 and 1.0")
	}
	if err := validateNonEmptyStrings("allowed-origin", o.AllowedOrigins); err != nil {
		return nil, err
	}

	body := map[string]any{
		"maxDurationSeconds": o.MaxDurationSeconds,
	}
	if o.Expiry != "" {
		body["expiry"] = o.Expiry
	}
	if o.Creator != "" {
		body["creator"] = o.Creator
	}
	if o.Name != "" {
		body["meta"] = map[string]string{"name": o.Name}
	}
	if len(o.AllowedOrigins) > 0 {
		body["allowedOrigins"] = o.AllowedOrigins
	}
	if o.RequireSignedURLsSet {
		body["requireSignedURLs"] = o.RequireSignedURLs
	}
	if o.ScheduledDeletion != "" {
		body["scheduledDeletion"] = o.ScheduledDeletion
	}
	if o.ThumbnailPctSet {
		body["thumbnailTimestampPct"] = o.ThumbnailTimestampPct
	}
	if o.WatermarkUID != "" {
		body["watermark"] = map[string]string{"uid": o.WatermarkUID}
	}
	return json.Marshal(body)
}

func newStreamTokenCmd(g *globalOpts) *cobra.Command {
	var exp int64
	var expiresIn string
	var downloadable bool
	cmd := &cobra.Command{
		Use:   "token <video-id>",
		Short: "Create a signed URL token for a Stream video",
		Long: `Create a short-lived signed URL token for a private Stream video.

Uses the Stream /token API (good for testing or low volume). Defaults match the
API: valid for one hour when no expiry is set.

Examples:

  cf stream token ea95132c15732412d22c1476fa83f27a
  cf stream token ea95132c15732412d22c1476fa83f27a --expires-in 30m
  cf stream token ea95132c15732412d22c1476fa83f27a --exp 1735689600 --downloadable`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := buildStreamTokenBody(streamTokenOpts{
				Exp:             exp,
				ExpSet:          cmd.Flags().Changed("exp"),
				ExpiresIn:       expiresIn,
				Downloadable:    downloadable,
				DownloadableSet: cmd.Flags().Changed("downloadable"),
				Now:             time.Now(),
			})
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := requireStreamAccountID(cfg)
			if err != nil {
				return err
			}
			req := api.Request{Method: "POST", Path: streamTokenPath(accountID, args[0]), Body: body}
			return runStreamRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().Int64Var(&exp, "exp", 0, "unix epoch after which the token is rejected (must be in the future, max 24h from now)")
	cmd.Flags().StringVar(&expiresIn, "expires-in", "", "token lifetime from now (e.g. 30m, 1h; max 24h); mutually exclusive with --exp")
	cmd.Flags().BoolVar(&downloadable, "downloadable", false, "allow the token to access MP4 download links")
	return cmd
}

type streamTokenOpts struct {
	Exp             int64
	ExpSet          bool
	ExpiresIn       string
	Downloadable    bool
	DownloadableSet bool
	Now             time.Time
}

// buildStreamTokenBody builds the JSON body for POST .../stream/{id}/token.
// A nil/empty body is valid for API defaults; we omit the body field when
// nothing was set by returning nil bytes.
func buildStreamTokenBody(o streamTokenOpts) ([]byte, error) {
	if o.ExpSet && o.ExpiresIn != "" {
		return nil, errors.New("specify only one of --exp or --expires-in")
	}
	now := o.Now
	if now.IsZero() {
		now = time.Now()
	}
	maxExp := now.Add(streamTokenMaxLifetime).Unix()

	body := map[string]any{}
	if o.ExpiresIn != "" {
		d, err := time.ParseDuration(o.ExpiresIn)
		if err != nil {
			return nil, fmt.Errorf("--expires-in: %w (use Go duration syntax, e.g. 30m, 1h)", err)
		}
		if d <= 0 {
			return nil, errors.New("--expires-in must be a positive duration")
		}
		if d > streamTokenMaxLifetime {
			return nil, errors.New("--expires-in cannot exceed 24h (Stream token limit)")
		}
		body["exp"] = now.Add(d).Unix()
	}
	if o.ExpSet {
		if err := validateStreamTokenExp(o.Exp, now, maxExp); err != nil {
			return nil, err
		}
		body["exp"] = o.Exp
	}
	if o.DownloadableSet {
		body["downloadable"] = o.Downloadable
	}
	if len(body) == 0 {
		// Empty body is accepted by the API (defaults to 1h token).
		return nil, nil
	}
	return json.Marshal(body)
}

// validateStreamTokenExp ensures --exp is strictly after now and within the
// Stream 24h issuance window (inclusive of the max boundary).
func validateStreamTokenExp(exp int64, now time.Time, maxExp int64) error {
	if exp <= now.Unix() {
		return errors.New("--exp must be a unix timestamp in the future")
	}
	if exp > maxExp {
		return errors.New("--exp cannot be more than 24h from now (Stream token limit)")
	}
	return nil
}

func runStreamRequest(cmd *cobra.Command, g *globalOpts, client *api.Client, req api.Request) error {
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
