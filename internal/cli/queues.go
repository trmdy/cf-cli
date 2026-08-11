package cli

// Queues porcelain covers the everyday queue, consumer, and message flows.
// See docs/STYLE.md; internal/cli/dns.go is the shape exemplar.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/trmdy/cf-cli/internal/api"
	"github.com/trmdy/cf-cli/internal/output"
)

type queueSummary struct {
	ID             string `json:"queue_id"`
	Name           string `json:"queue_name"`
	ConsumersTotal int    `json:"consumers_total_count"`
	Settings       struct {
		DeliveryPaused bool `json:"delivery_paused"`
	} `json:"settings"`
}

func newQueuesCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{Use: "queues", Short: "Manage Cloudflare Queues"}
	cmd.AddCommand(
		newQueuesListCmd(g),
		newQueuesGetCmd(g),
		newQueuesCreateCmd(g),
		newQueuesUpdateCmd(g),
		newQueuesDeleteCmd(g),
		newQueuesConsumerCmd(g),
		newQueuesMessageCmd(g),
	)
	return cmd
}

func queuesPath(accountID string) string { return "/accounts/" + url.PathEscape(accountID) + "/queues" }

func queuePath(accountID, queue string) string {
	return queuesPath(accountID) + "/" + url.PathEscape(queue)
}

func requireQueueAccountID(accountID string) error {
	if accountID == "" {
		return errors.New("missing account ID: pass --account-id, set CLOUDFLARE_ACCOUNT_ID, or configure a profile")
	}
	return nil
}

func newQueuesListCmd(g *globalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List queues in the account",
		Long:  "List queues in the account.\n\nExample:\n\n  cf queues list --account-id $CLOUDFLARE_ACCOUNT_ID",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			if err := requireQueueAccountID(cfg.AccountID); err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: queuesPath(cfg.AccountID)}
			if g.DryRun {
				return runQueuesRequest(cmd, g, client, req)
			}
			env, err := client.DoAutoPaginate(cmd.Context(), req)
			if err != nil {
				return err
			}
			format := g.format(output.Table)
			if g.Query != "" || format != output.Table {
				return g.renderResult(cmd, env.Result, output.JSON)
			}
			var queues []queueSummary
			if err := json.Unmarshal(env.Result, &queues); err != nil {
				return output.RenderRaw(cmd.OutOrStdout(), output.JSON, env.Result)
			}
			rows := make([][]string, 0, len(queues))
			for _, q := range queues {
				rows = append(rows, []string{q.ID, q.Name, fmt.Sprintf("%d", q.ConsumersTotal), fmt.Sprintf("%t", q.Settings.DeliveryPaused)})
			}
			return output.RenderTable(cmd.OutOrStdout(), []string{"ID", "NAME", "CONSUMERS", "PAUSED"}, rows)
		},
	}
}

func newQueuesGetCmd(g *globalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "get <queue>",
		Short: "Show a queue by name or ID",
		Long:  "Show a queue by name or ID.\n\nExample:\n\n  cf queues get events",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			if err := requireQueueAccountID(cfg.AccountID); err != nil {
				return err
			}
			return runQueuesRequest(cmd, g, client, api.Request{Method: "GET", Path: queuePath(cfg.AccountID, args[0])})
		},
	}
}

func newQueuesCreateCmd(g *globalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "create <queue-name>",
		Short: "Create a queue",
		Long:  "Create a queue.\n\nExample:\n\n  cf queues create events",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			if err := requireQueueAccountID(cfg.AccountID); err != nil {
				return err
			}
			body, err := json.Marshal(map[string]string{"queue_name": args[0]})
			if err != nil {
				return err
			}
			return runQueuesRequest(cmd, g, client, api.Request{Method: "POST", Path: queuesPath(cfg.AccountID), Body: body})
		},
	}
}

func newQueuesUpdateCmd(g *globalOpts) *cobra.Command {
	var name string
	var deliveryDelay, retentionPeriod int
	var deliveryPaused bool
	cmd := &cobra.Command{
		Use:   "update <queue>",
		Short: "Update queue settings",
		Long: `Update a queue name or settings.

Examples:

  cf queues update events --delivery-paused
  cf queues update events --delivery-delay 30 --message-retention-period 86400`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := buildQueueUpdateBody(cmd, name, deliveryDelay, retentionPeriod, deliveryPaused)
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			if err := requireQueueAccountID(cfg.AccountID); err != nil {
				return err
			}
			return runQueuesRequest(cmd, g, client, api.Request{Method: "PATCH", Path: queuePath(cfg.AccountID, args[0]), Body: body})
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "new queue name")
	cmd.Flags().IntVar(&deliveryDelay, "delivery-delay", 0, "seconds to delay delivery to consumers")
	cmd.Flags().BoolVar(&deliveryPaused, "delivery-paused", false, "pause delivery to consumers")
	cmd.Flags().IntVar(&retentionPeriod, "message-retention-period", 0, "seconds to retain unconsumed messages")
	return cmd
}

func buildQueueUpdateBody(cmd *cobra.Command, name string, deliveryDelay, retentionPeriod int, deliveryPaused bool) ([]byte, error) {
	body := map[string]any{}
	if cmd.Flags().Changed("name") {
		body["queue_name"] = name
	}
	settings := map[string]any{}
	if cmd.Flags().Changed("delivery-delay") {
		settings["delivery_delay"] = deliveryDelay
	}
	if cmd.Flags().Changed("delivery-paused") {
		settings["delivery_paused"] = deliveryPaused
	}
	if cmd.Flags().Changed("message-retention-period") {
		settings["message_retention_period"] = retentionPeriod
	}
	if len(settings) > 0 {
		body["settings"] = settings
	}
	if len(body) == 0 {
		return nil, errors.New("nothing to update: pass --name, --delivery-delay, --delivery-paused, or --message-retention-period")
	}
	return json.Marshal(body)
}

func newQueuesDeleteCmd(g *globalOpts) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <queue>",
		Short: "Delete a queue",
		Long:  "Delete a queue.\n\nExample:\n\n  cf queues delete events --force",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			if err := requireQueueAccountID(cfg.AccountID); err != nil {
				return err
			}
			if !force && !g.DryRun && !confirm(fmt.Sprintf("Delete queue %s?", args[0])) {
				return errors.New("aborted (pass --force to skip confirmation)")
			}
			return runQueuesRequest(cmd, g, client, api.Request{Method: "DELETE", Path: queuePath(cfg.AccountID, args[0])})
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	return cmd
}

func newQueuesConsumerCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{Use: "consumer", Short: "Manage queue consumers"}
	cmd.AddCommand(newQueuesConsumerAddCmd(g), newQueuesConsumerRemoveCmd(g))
	return cmd
}

func newQueuesConsumerAddCmd(g *globalOpts) *cobra.Command {
	var consumerType, script, deadLetterQueue string
	var batchSize, maxConcurrency, maxRetries, maxWaitTime, retryDelay, visibilityTimeout int
	cmd := &cobra.Command{
		Use:   "add <queue>",
		Short: "Add a worker or HTTP pull consumer",
		Long: `Add a consumer to a queue.

Examples:

  cf queues consumer add events --script process-events
  cf queues consumer add events --type http-pull --batch-size 10 --visibility-timeout-ms 30000`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := buildQueueConsumerBody(cmd, consumerType, script, deadLetterQueue, batchSize, maxConcurrency, maxRetries, maxWaitTime, retryDelay, visibilityTimeout)
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			if err := requireQueueAccountID(cfg.AccountID); err != nil {
				return err
			}
			return runQueuesRequest(cmd, g, client, api.Request{Method: "POST", Path: queuePath(cfg.AccountID, args[0]) + "/consumers", Body: body})
		},
	}
	cmd.Flags().StringVar(&consumerType, "type", "worker", "consumer type: worker or http-pull")
	cmd.Flags().StringVar(&script, "script", "", "Worker script name (required for worker consumers)")
	cmd.Flags().StringVar(&deadLetterQueue, "dead-letter-queue", "", "queue name for failed messages")
	cmd.Flags().IntVar(&batchSize, "batch-size", 0, "maximum messages in a batch")
	cmd.Flags().IntVar(&maxConcurrency, "max-concurrency", 0, "maximum concurrent worker consumers")
	cmd.Flags().IntVar(&maxRetries, "max-retries", 0, "maximum delivery retries")
	cmd.Flags().IntVar(&maxWaitTime, "max-wait-time-ms", 0, "maximum milliseconds to wait for a batch")
	cmd.Flags().IntVar(&retryDelay, "retry-delay", 0, "seconds to wait before retrying a message")
	cmd.Flags().IntVar(&visibilityTimeout, "visibility-timeout-ms", 0, "milliseconds a pulled message stays leased")
	return cmd
}

func buildQueueConsumerBody(cmd *cobra.Command, consumerType, script, deadLetterQueue string, batchSize, maxConcurrency, maxRetries, maxWaitTime, retryDelay, visibilityTimeout int) ([]byte, error) {
	consumerType = strings.ToLower(strings.TrimSpace(consumerType))
	if consumerType == "http-pull" {
		consumerType = "http_pull"
	}
	if consumerType != "worker" && consumerType != "http_pull" {
		return nil, errors.New("--type must be worker or http-pull")
	}
	if consumerType == "worker" && strings.TrimSpace(script) == "" {
		return nil, errors.New("--script is required for worker consumers")
	}
	if consumerType == "http_pull" && cmd.Flags().Changed("script") {
		return nil, errors.New("--script is only valid for worker consumers")
	}
	if consumerType != "worker" && (cmd.Flags().Changed("max-concurrency") || cmd.Flags().Changed("max-wait-time-ms")) {
		return nil, errors.New("--max-concurrency and --max-wait-time-ms are only valid for worker consumers")
	}
	if consumerType != "http_pull" && cmd.Flags().Changed("visibility-timeout-ms") {
		return nil, errors.New("--visibility-timeout-ms is only valid for http-pull consumers")
	}
	body := map[string]any{"type": consumerType}
	if consumerType == "worker" {
		body["script_name"] = script
	}
	if cmd.Flags().Changed("dead-letter-queue") {
		body["dead_letter_queue"] = deadLetterQueue
	}
	settings := map[string]any{}
	for _, setting := range []struct {
		flag  string
		key   string
		value int
	}{
		{"batch-size", "batch_size", batchSize},
		{"max-retries", "max_retries", maxRetries},
		{"retry-delay", "retry_delay", retryDelay},
	} {
		if cmd.Flags().Changed(setting.flag) {
			settings[setting.key] = setting.value
		}
	}
	if consumerType == "worker" {
		if cmd.Flags().Changed("max-concurrency") {
			settings["max_concurrency"] = maxConcurrency
		}
		if cmd.Flags().Changed("max-wait-time-ms") {
			settings["max_wait_time_ms"] = maxWaitTime
		}
	} else if cmd.Flags().Changed("visibility-timeout-ms") {
		settings["visibility_timeout_ms"] = visibilityTimeout
	}
	if len(settings) > 0 {
		body["settings"] = settings
	}
	return json.Marshal(body)
}

func newQueuesConsumerRemoveCmd(g *globalOpts) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "remove <queue> <consumer-id>",
		Short: "Remove a queue consumer",
		Long:  "Remove a queue consumer.\n\nExample:\n\n  cf queues consumer remove events consumer-id --force",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			if err := requireQueueAccountID(cfg.AccountID); err != nil {
				return err
			}
			if !force && !g.DryRun && !confirm(fmt.Sprintf("Remove consumer %s from queue %s?", args[1], args[0])) {
				return errors.New("aborted (pass --force to skip confirmation)")
			}
			path := queuePath(cfg.AccountID, args[0]) + "/consumers/" + url.PathEscape(args[1])
			return runQueuesRequest(cmd, g, client, api.Request{Method: "DELETE", Path: path})
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	return cmd
}

func newQueuesMessageCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{Use: "message", Short: "Send, pull, and acknowledge queue messages"}
	cmd.AddCommand(newQueuesMessageSendCmd(g), newQueuesMessagePullCmd(g), newQueuesMessageAckCmd(g))
	return cmd
}

func newQueuesMessageSendCmd(g *globalOpts) *cobra.Command {
	var jsonBody bool
	var delaySeconds int
	cmd := &cobra.Command{
		Use:   "send <queue> <body>",
		Short: "Send one message to a queue",
		Long: `Send one message to a queue.

Examples:

  cf queues message send events "hello"
  cf queues message send events '{"event":"created"}' --json --delay-seconds 30`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := buildQueueMessageBody(args[1], jsonBody, delaySeconds, cmd.Flags().Changed("delay-seconds"))
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			if err := requireQueueAccountID(cfg.AccountID); err != nil {
				return err
			}
			return runQueuesRequest(cmd, g, client, api.Request{Method: "POST", Path: queuePath(cfg.AccountID, args[0]) + "/messages", Body: body})
		},
	}
	cmd.Flags().BoolVar(&jsonBody, "json", false, "treat body as a JSON object")
	cmd.Flags().IntVar(&delaySeconds, "delay-seconds", 0, "seconds to delay delivery")
	return cmd
}

func buildQueueMessageBody(body string, jsonBody bool, delaySeconds int, includeDelay bool) ([]byte, error) {
	payload := map[string]any{}
	if jsonBody {
		var value map[string]any
		if err := json.Unmarshal([]byte(body), &value); err != nil || value == nil {
			return nil, errors.New("--json body must be a valid JSON object")
		}
		payload["body"] = value
		payload["content_type"] = "json"
	} else {
		payload["body"] = body
		payload["content_type"] = "text"
	}
	if includeDelay {
		payload["delay_seconds"] = delaySeconds
	}
	return json.Marshal(payload)
}

func newQueuesMessagePullCmd(g *globalOpts) *cobra.Command {
	var batchSize, visibilityTimeout int
	cmd := &cobra.Command{
		Use:   "pull <queue>",
		Short: "Pull messages from a queue",
		Long:  "Pull messages from a queue.\n\nExample:\n\n  cf queues message pull events --batch-size 10 --visibility-timeout-ms 30000",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := buildQueuePullBody(cmd, batchSize, visibilityTimeout)
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			if err := requireQueueAccountID(cfg.AccountID); err != nil {
				return err
			}
			return runQueuesRequest(cmd, g, client, api.Request{Method: "POST", Path: queuePath(cfg.AccountID, args[0]) + "/messages/pull", Body: body})
		},
	}
	cmd.Flags().IntVar(&batchSize, "batch-size", 0, "maximum messages to pull")
	cmd.Flags().IntVar(&visibilityTimeout, "visibility-timeout-ms", 0, "milliseconds each message stays leased")
	return cmd
}

func buildQueuePullBody(cmd *cobra.Command, batchSize, visibilityTimeout int) ([]byte, error) {
	body := map[string]int{}
	if cmd.Flags().Changed("batch-size") {
		body["batch_size"] = batchSize
	}
	if cmd.Flags().Changed("visibility-timeout-ms") {
		body["visibility_timeout_ms"] = visibilityTimeout
	}
	return json.Marshal(body)
}

func newQueuesMessageAckCmd(g *globalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "ack <queue> <lease-id> [lease-id...]",
		Short: "Acknowledge pulled queue messages",
		Long:  "Acknowledge one or more pulled messages by lease ID.\n\nExample:\n\n  cf queues message ack events lease-id-1 lease-id-2",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := buildQueueAckBody(args[1:])
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			if err := requireQueueAccountID(cfg.AccountID); err != nil {
				return err
			}
			return runQueuesRequest(cmd, g, client, api.Request{Method: "POST", Path: queuePath(cfg.AccountID, args[0]) + "/messages/ack", Body: body})
		},
	}
}

func buildQueueAckBody(leaseIDs []string) ([]byte, error) {
	acks := make([]map[string]string, 0, len(leaseIDs))
	for _, leaseID := range leaseIDs {
		if strings.TrimSpace(leaseID) == "" {
			return nil, errors.New("lease IDs cannot be empty")
		}
		acks = append(acks, map[string]string{"lease_id": leaseID})
	}
	return json.Marshal(map[string]any{"acks": acks})
}

func runQueuesRequest(cmd *cobra.Command, g *globalOpts, client *api.Client, req api.Request) error {
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
