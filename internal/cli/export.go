package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"ok-gobot/internal/config"
	"ok-gobot/internal/storage"
)

func newExportCommand(cfg *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export data from ok-gobot storage",
	}
	cmd.AddCommand(newExportTrainingDataCommand(cfg))
	return cmd
}

// trainingMessage matches the OpenAI fine-tuning message format.
type trainingMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// trainingExample is one JSONL record in the OpenAI fine-tuning format.
type trainingExample struct {
	Messages []trainingMessage `json:"messages"`
}

func newExportTrainingDataCommand(cfg *config.Config) *cobra.Command {
	var (
		since      string
		until      string
		minMsgs    int
		requireJob bool
		output     string
	)

	cmd := &cobra.Command{
		Use:   "training-data",
		Short: "Export conversation history as OpenAI fine-tuning JSONL",
		Long: `Export sessions from SQLite as a JSONL file in OpenAI fine-tuning format.

Each line of the output file contains one training example:
  {"messages": [{"role": "user", "content": "..."}, {"role": "assistant", "content": "..."}]}

Filters:
  --since / --until   date range (YYYY-MM-DD)
  --min-msgs          minimum messages per session (default 4)
  --require-job       only sessions that ran through the agent job loop
                      (sessions that have tool-use activity)

Example usage:
  ok-gobot export training-data --since 2025-01-01 --min-msgs 6
  ok-gobot export training-data --require-job --output my-data.jsonl`,
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := storage.New(cfg.StoragePath)
			if err != nil {
				return fmt.Errorf("failed to open storage: %w", err)
			}
			defer store.Close() //nolint:errcheck

			filter := storage.ExportFilter{
				Since:      since,
				Until:      until,
				MinMsgs:    minMsgs,
				RequireJob: requireJob,
			}

			sessions, err := store.ListSessionsForExport(filter)
			if err != nil {
				return fmt.Errorf("failed to query sessions: %w", err)
			}

			if output == "" {
				output = fmt.Sprintf("training-data-%s.jsonl", time.Now().Format("2006-01-02"))
			}

			f, err := os.Create(output)
			if err != nil {
				return fmt.Errorf("failed to create output file: %w", err)
			}
			defer f.Close() //nolint:errcheck

			enc := json.NewEncoder(f)
			written := 0

			for _, sess := range sessions {
				example, ok := buildTrainingExample(sess)
				if !ok {
					continue
				}
				if err := enc.Encode(example); err != nil {
					return fmt.Errorf("failed to write record: %w", err)
				}
				written++
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Exported %d training examples to %s\n", written, output)
			return nil
		},
	}

	cmd.Flags().StringVar(&since, "since", "", "include sessions created on or after this date (YYYY-MM-DD)")
	cmd.Flags().StringVar(&until, "until", "", "include sessions created on or before this date (YYYY-MM-DD)")
	cmd.Flags().IntVar(&minMsgs, "min-msgs", 4, "minimum number of messages a session must have")
	cmd.Flags().BoolVar(&requireJob, "require-job", false, "only include sessions that ran through the agent job loop")
	cmd.Flags().StringVarP(&output, "output", "o", "", "output file path (default: training-data-YYYY-MM-DD.jsonl)")

	return cmd
}

// buildTrainingExample converts a session into an OpenAI fine-tuning example.
// Only user and assistant messages are included. Returns false if there are no
// valid message pairs.
func buildTrainingExample(sess storage.ExportSession) (trainingExample, bool) {
	var msgs []trainingMessage
	for _, m := range sess.Messages {
		if m.Role != "user" && m.Role != "assistant" {
			continue
		}
		content := m.Content
		if content == "" {
			continue
		}
		msgs = append(msgs, trainingMessage{Role: m.Role, Content: content})
	}

	// Need at least one user+assistant pair.
	if len(msgs) < 2 {
		return trainingExample{}, false
	}

	return trainingExample{Messages: msgs}, true
}
