package cmd

import (
	"fmt"

	"github.com/chanzuckerberg/czecs/version"
	"github.com/spf13/cobra"
)

type versionCmd struct{}

func init() {
	rootCmd.AddCommand(newVersionCmd())
}

func newVersionCmd() *cobra.Command {
	version := &versionCmd{}
	cmd := &cobra.Command{
		Use:          "version",
		Short:        "Print the version number of czecs",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return version.run()
		},
	}
	return cmd
}

func (v *versionCmd) run() error {
	ver, err := version.VersionString()
	if err != nil {
		return err
	}
	fmt.Println(ver)
	return nil
}
