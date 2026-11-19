package cmd

import (
	"github.com/spf13/cobra"
)

func init() {
	configCmd.AddCommand(configExportCmd)
}

var configExportCmd = &cobra.Command{
	Use:   "export <path>",
	Short: "Export the configuration to a file",
	Long: `Export the configuration to a file. The path must be for a
json or yaml file. This exported configuration can be changed,
and imported again with 'config import' command.

The file holds the key that signs the sessions of the users, and
what the authentication method keeps secret. It is written so that
only its owner can read it, also when it replaces a file that
others could read.`,
	Args: jsonYamlArg,
	RunE: withStore(func(_ *cobra.Command, args []string, st *store) error {
		settings, err := st.Settings.Get()
		if err != nil {
			return err
		}

		server, err := st.Settings.GetServer()
		if err != nil {
			return err
		}

		auther, err := st.Auth.Get(settings.AuthMethod)
		if err != nil {
			return err
		}

		data := &settingsFile{
			Settings: settings,
			Auther:   auther,
			Server:   server,
		}

		// The file holds the key that signs sessions.
		err = marshalPrivate(args[0], data)
		if err != nil {
			return err
		}
		return nil
	}, storeOptions{}),
}
