package cmd

import (
	"github.com/spf13/cobra"
)

func init() {
	usersCmd.AddCommand(usersExportCmd)
}

var usersExportCmd = &cobra.Command{
	Use:   "export <path>",
	Short: "Export all users to a file.",
	Long: `Export all users to a json or yaml file. Please indicate the
path to the file where you want to write the users.

The file holds the password hashes of the users. It is written so
that only its owner can read it, also when it replaces a file that
others could read.`,
	Args: jsonYamlArg,
	RunE: withStore(func(_ *cobra.Command, args []string, st *store) error {
		list, err := st.Users.Gets("", false)
		if err != nil {
			return err
		}

		// The file holds the password hashes.
		err = marshalPrivate(args[0], list)
		if err != nil {
			return err
		}
		return nil
	}, storeOptions{}),
}
