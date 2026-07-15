package journal

import (
	"github.com/spf13/cobra"

	"dyna-agent/internal/runstore"
)

func NewCommand() *cobra.Command {
	var kind, next string
	cmd := &cobra.Command{
		Use:   "journal <message>",
		Short: "Append a progress entry to this worker's journal",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return runstore.AppendAgentJournalFromEnv(kind, args[0], next)
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "update", "entry kind (for example: update, finding, decision, verification, blocker)")
	cmd.Flags().StringVar(&next, "next", "", "concise next step")
	return cmd
}
