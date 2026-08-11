package cli

// R2 porcelain: the everyday bucket workflows.
// Object commands are deliberately excluded because complete R2 transfers need
// streaming. api.Client.DoRaw buffers and caps each transfer at 100 MiB.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/trmdy/cf-cli/internal/api"
	"github.com/trmdy/cf-cli/internal/output"
)

type r2Bucket struct {
	Name         string `json:"name"`
	CreationDate string `json:"creation_date"`
	Location     string `json:"location"`
}

type r2BucketList struct {
	Buckets []r2Bucket `json:"buckets"`
	Cursor  string     `json:"cursor"`
}

const r2MaxPages = 1000

func newR2Cmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "r2",
		Short: "Manage R2 buckets",
	}
	cmd.AddCommand(
		newR2ListCmd(g),
		newR2CreateCmd(g),
		newR2DeleteCmd(g),
		newR2InfoCmd(g),
	)
	return cmd
}

func r2BucketsPath(accountID string) string {
	return "/accounts/" + url.PathEscape(accountID) + "/r2/buckets"
}

func r2BucketPath(accountID, bucket string) string {
	return r2BucketsPath(accountID) + "/" + url.PathEscape(bucket)
}

func r2AccountID(accountID string) (string, error) {
	if strings.TrimSpace(accountID) == "" {
		return "", errors.New("no account specified: pass --account-id, set CLOUDFLARE_ACCOUNT_ID, or configure a profile")
	}
	return accountID, nil
}

func r2BucketName(bucketName string) (string, error) {
	bucketName = strings.TrimSpace(bucketName)
	if bucketName == "" {
		return "", errors.New("bucket name cannot be empty")
	}
	return bucketName, nil
}

func r2ListBuckets(ctx context.Context, client *api.Client, req api.Request) ([]r2Bucket, error) {
	var buckets []r2Bucket
	for page := 1; page <= r2MaxPages; page++ {
		env, err := client.Do(ctx, req)
		if err != nil {
			return nil, err
		}
		var result r2BucketList
		if err := json.Unmarshal(env.Result, &result); err != nil {
			return nil, fmt.Errorf("list R2 buckets: unexpected response: %w", err)
		}
		buckets = append(buckets, result.Buckets...)
		if result.Cursor == "" {
			return buckets, nil
		}
		req.Query.Set("cursor", result.Cursor)
	}
	return nil, fmt.Errorf("list R2 buckets: stopped after %d pages", r2MaxPages)
}

func newR2ListCmd(g *globalOpts) *cobra.Command {
	var nameContains string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List R2 buckets",
		Long:  "List R2 buckets in the configured account.\n\nExamples:\n\n  cf r2 list\n  cf r2 list --name-contains backups",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := r2AccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			query := url.Values{"per_page": {"100"}}
			if strings.TrimSpace(nameContains) != "" {
				query.Set("name_contains", nameContains)
			}
			req := api.Request{Method: "GET", Path: r2BucketsPath(accountID), Query: query}
			if g.DryRun {
				dump, err := client.Dump(req)
				if err != nil {
					return err
				}
				return g.renderValue(cmd, dump, output.JSON)
			}
			buckets, err := r2ListBuckets(cmd.Context(), client, req)
			if err != nil {
				return err
			}
			result, err := json.Marshal(r2BucketList{Buckets: buckets})
			if err != nil {
				return err
			}
			format := g.format(output.Table)
			if g.Query != "" || format != output.Table {
				return g.renderResult(cmd, result, output.JSON)
			}
			rows := make([][]string, 0, len(buckets))
			for _, bucket := range buckets {
				rows = append(rows, []string{bucket.Name, bucket.CreationDate, bucket.Location})
			}
			return output.RenderTable(cmd.OutOrStdout(), []string{"NAME", "CREATED", "LOCATION"}, rows)
		},
	}
	cmd.Flags().StringVar(&nameContains, "name-contains", "", "filter buckets by name")
	return cmd
}

func newR2CreateCmd(g *globalOpts) *cobra.Command {
	var location string
	cmd := &cobra.Command{
		Use:   "create <bucket-name>",
		Short: "Create an R2 bucket",
		Long:  "Create an R2 bucket.\n\nExamples:\n\n  cf r2 create backups\n  cf r2 create eu-assets --location weur",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			bucketName, err := r2BucketName(args[0])
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := r2AccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			body := map[string]string{"name": bucketName}
			if cmd.Flags().Changed("location") {
				if strings.TrimSpace(location) == "" {
					return errors.New("--location cannot be empty")
				}
				body["locationHint"] = location
			}
			raw, err := json.Marshal(body)
			if err != nil {
				return err
			}
			req := api.Request{Method: "POST", Path: r2BucketsPath(accountID), Body: raw}
			return runR2Request(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&location, "location", "", "location hint for the bucket (for example, weur)")
	return cmd
}

func newR2DeleteCmd(g *globalOpts) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <bucket-name>",
		Short: "Delete an R2 bucket",
		Long:  "Delete an R2 bucket. The bucket must be empty.\n\nExamples:\n\n  cf r2 delete old-backups --force",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			bucketName, err := r2BucketName(args[0])
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := r2AccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			if !force && !g.DryRun {
				if !confirm(fmt.Sprintf("Delete R2 bucket %s?", bucketName)) {
					return errors.New("aborted (pass --force to skip confirmation)")
				}
			}
			req := api.Request{Method: "DELETE", Path: r2BucketPath(accountID, bucketName)}
			return runR2Request(cmd, g, client, req)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	return cmd
}

func newR2InfoCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "info <bucket-name>",
		Short: "Show R2 bucket details",
		Long:  "Show R2 bucket details.\n\nExamples:\n\n  cf r2 info backups",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			bucketName, err := r2BucketName(args[0])
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := r2AccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: r2BucketPath(accountID, bucketName)}
			return runR2Request(cmd, g, client, req)
		},
	}
	return cmd
}

func runR2Request(cmd *cobra.Command, g *globalOpts, client *api.Client, req api.Request) error {
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
