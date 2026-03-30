package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"ok-gobot/internal/config"
	"ok-gobot/internal/storage"
)

// trainingMessage is one message in the OpenAI fine-tune JSONL format.
type trainingMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// trainingExample is one line of the JSONL file.
type trainingExample struct {
	Messages []trainingMessage `json:"messages"`
}

func newExportCommand(cfg *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export data from ok-gobot",
	}
	cmd.AddCommand(newExportTrainingDataCommand(cfg))
	return cmd
}

func newExportTrainingDataCommand(cfg *config.Config) *cobra.Command {
	var (
		since       string
		until       string
		minMessages int
		withJobs    bool
		successOnly bool
		output      string
		systemMsg   string
	)

	cmd := &cobra.Command{
		Use:   "training-data",
		Short: "Export conversations as OpenAI fine-tuning JSONL",
		Long: `Export conversation history from SQLite to OpenAI fine-tuning JSONL format.

Each line of the output file is a JSON object:
  {"messages": [{"role": "user", "content": "..."}, {"role": "assistant", "content": "..."}]}

Filters allow you to select only high-quality sessions:
  --since / --until   restrict by session creation date (YYYY-MM-DD)
  --min-messages      skip short sessions (default: 4)
  --with-jobs         only sessions processed through the job system (proxy for tool use)
  --successful-only   additionally require at least one succeeded job

The output file defaults to training-data-YYYY-MM-DD.jsonl in the current directory.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := storage.New(cfg.StoragePath)
			if err != nil {
				return fmt.Errorf("failed to open storage: %w", err)
			}
			defer store.Close() //nolint:errcheck

			filter := storage.TrainingFilter{
				MinMessages: minMessages,
				WithJobs:    withJobs,
				SuccessOnly: successOnly,
			}

			if since != "" {
				t, err := time.Parse("2006-01-02", since)
				if err != nil {
					return fmt.Errorf("invalid --since date %q: use YYYY-MM-DD", since)
				}
				filter.Since = t
			}
			if until != "" {
				t, err := time.Parse("2006-01-02", until)
				if err != nil {
					return fmt.Errorf("invalid --until date %q: use YYYY-MM-DD", until)
				}
				// Include the entire until day.
				filter.Until = t.Add(24*time.Hour - time.Second)
			}

			sessions, err := store.ListTrainingSessions(filter)
			if err != nil {
				return fmt.Errorf("failed to query sessions: %w", err)
			}

			if len(sessions) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No sessions match the given filters.")
				return nil
			}

			// Determine output file name.
			outFile := output
			if outFile == "" {
				outFile = "training-data-" + time.Now().Format("2006-01-02") + ".jsonl"
			}

			f, err := os.Create(outFile)
			if err != nil {
				return fmt.Errorf("failed to create output file: %w", err)
			}
			defer f.Close() //nolint:errcheck

			enc := json.NewEncoder(f)

			var exported, skipped int
			for _, sess := range sessions {
				msgs, err := store.GetTrainingMessages(sess.SessionKey)
				if err != nil {
					return fmt.Errorf("failed to get messages for session %s: %w", sess.SessionKey, err)
				}

				// Build the message list for this example.
				var tmMsgs []trainingMessage

				if systemMsg != "" {
					tmMsgs = append(tmMsgs, trainingMessage{Role: "system", Content: systemMsg})
				}

				for _, m := range msgs {
					content := strings.TrimSpace(m.Content)
					if content == "" {
						continue
					}
					tmMsgs = append(tmMsgs, trainingMessage{
						Role:    m.Role,
						Content: content,
					})
				}

				// Need at least one user + one assistant turn.
				if len(tmMsgs) < 2 {
					skipped++
					continue
				}
				// Validate structure: must end with an assistant message.
				if tmMsgs[len(tmMsgs)-1].Role != "assistant" {
					skipped++
					continue
				}

				example := trainingExample{Messages: tmMsgs}
				if err := enc.Encode(example); err != nil {
					return fmt.Errorf("failed to write example: %w", err)
				}
				exported++
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Exported %d examples to %s", exported, outFile)
			if skipped > 0 {
				fmt.Fprintf(out, " (%d sessions skipped — incomplete turns)", skipped)
			}
			fmt.Fprintln(out)
			return nil
		},
	}

	cmd.Flags().StringVar(&since, "since", "", "only sessions created on or after this date (YYYY-MM-DD)")
	cmd.Flags().StringVar(&until, "until", "", "only sessions created on or before this date (YYYY-MM-DD)")
	cmd.Flags().IntVar(&minMessages, "min-messages", 4, "minimum number of messages a session must have")
	cmd.Flags().BoolVar(&withJobs, "with-jobs", false, "only include sessions processed through the job system (proxy for tool use)")
	cmd.Flags().BoolVar(&successOnly, "successful-only", false, "only include sessions with at least one succeeded job (implies --with-jobs)")
	cmd.Flags().StringVar(&output, "output", "", "output file path (default: training-data-YYYY-MM-DD.jsonl)")
	cmd.Flags().StringVar(&systemMsg, "system", "", "optional system prompt to prepend to every training example")

	return cmd
}
