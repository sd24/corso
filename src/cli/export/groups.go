package export

import (
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/alcionai/corso/src/cli/flags"
	. "github.com/alcionai/corso/src/cli/print"
	"github.com/alcionai/corso/src/cli/utils"
)

// called by export.go to map subcommands to provider-specific handling.
func addGroupsCommands(cmd *cobra.Command) *cobra.Command {
	var (
		c  *cobra.Command
		fs *pflag.FlagSet
	)

	switch cmd.Use {
	case exportCommand:
		c, fs = utils.AddCommand(cmd, groupsExportCmd(), utils.MarkPreReleaseCommand())

		c.Use = c.Use + " " + groupsServiceCommandUseSuffix

		// Flags addition ordering should follow the order we want them to appear in help and docs:
		// More generic (ex: --user) and more frequently used flags take precedence.
		fs.SortFlags = false

		flags.AddBackupIDFlag(c, true)
		flags.AddExportConfigFlags(c)
		flags.AddFailFastFlag(c)
		flags.AddCorsoPassphaseFlags(c)
		flags.AddAWSCredsFlags(c)
	}

	return c
}

// TODO: correct examples
const (
	groupsServiceCommand          = "groups"
	groupsServiceCommandUseSuffix = "<destination> --backup <backupId>"

	//nolint:lll
	groupsServiceCommandExportExamples = `# Export file with ID 98765abcdef in Bob's last backup (1234abcd...) to my-exports directory
corso export groups my-exports --backup 1234abcd-12ab-cd34-56de-1234abcd --file 98765abcdef

# Export files named "FY2021 Planning.xlsx" in "Documents/Finance Reports" to current directory
corso export groups . --backup 1234abcd-12ab-cd34-56de-1234abcd \
    --file "FY2021 Planning.xlsx" --folder "Documents/Finance Reports"

# Export all files and folders in folder "Documents/Finance Reports" that were created before 2020 to my-exports
corso export groups my-exports --backup 1234abcd-12ab-cd34-56de-1234abcd
    --folder "Documents/Finance Reports" --file-created-before 2020-01-01T00:00:00`
)

// `corso export groups [<flag>...] <destination>`
func groupsExportCmd() *cobra.Command {
	return &cobra.Command{
		Use:   groupsServiceCommand,
		Short: "Export M365 Groups service data",
		RunE:  exportGroupsCmd,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return errors.New("missing export destination")
			}

			return nil
		},
		Example: groupsServiceCommandExportExamples,
	}
}

// processes an groups service export.
func exportGroupsCmd(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	if utils.HasNoFlagsAndShownHelp(cmd) {
		return nil
	}

	return Only(ctx, utils.ErrNotYetImplemented)
}