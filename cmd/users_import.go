package cmd

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	fberrors "github.com/werewolfboy13/filebrowser/v3/errors"
	"github.com/werewolfboy13/filebrowser/v3/users"
)

func init() {
	usersCmd.AddCommand(usersImportCmd)
	usersImportCmd.Flags().Bool("overwrite", false, "replace the registered users whose ID and username are those of a user in the file")
	usersImportCmd.Flags().Bool("replace", false, "replace the entire user base")
}

var usersImportCmd = &cobra.Command{
	Use:   "import <path>",
	Short: "Import users from a file",
	Long: `Import users from a file. The path must be for a json or yaml
file.

Without a flag, the users in the file are added as new users. The
IDs they have in the file are not used: every new user gets an ID
from this installation. A user whose username is registered already
is refused.

With --overwrite, a user in the file that has both the ID and the
username of a registered user replaces that user. A user that has
the ID of a registered user and another username is refused: an ID
from another installation says nothing about who the user is here,
and a user is renamed with "users update". To add such a user as a
new one, leave its ID out of the file or set it to 0. The other
users in the file are added as new users, as without the flag.

With --replace, the registered users are written to
users.backup.json in the working directory and deleted, and the
users in the file are added as new users. It can be used only while
no registered user is an administrator, as in a database that has
no users yet: the last administrator cannot be deleted, so with an
administrator registered the command refuses, before it writes the
backup or deletes a user. To change the users of such a database,
use --overwrite, and remove users with "users rm".

The file is checked as a whole first: if one of its users is
refused, or it has a username twice, nothing is imported and no
user is replaced or deleted.`,
	Args: jsonYamlArg,
	RunE: withStore(func(cmd *cobra.Command, args []string, st *store) error {
		flags := cmd.Flags()
		fd, err := os.Open(args[0])
		if err != nil {
			return err
		}
		defer fd.Close()

		list := []*users.User{}
		err = unmarshal(args[0], &list)
		if err != nil {
			return err
		}

		for i, user := range list {
			if user == nil {
				return fmt.Errorf("can't import the users: entry %d of the file is null, not a user", i+1)
			}
			err = user.Clean("", false)
			if err != nil {
				return err
			}
		}

		replace, err := flags.GetBool("replace")
		if err != nil {
			return err
		}

		overwrite, err := flags.GetBool("overwrite")
		if err != nil {
			return err
		}

		oldUsers, err := st.Users.Gets("", false)
		if err != nil && !errors.Is(err, fberrors.ErrNotExist) {
			return err
		}

		// Before anything is changed. With --replace, none of the
		// registered users is left by the time the file is imported.
		if replace {
			err = prepareUsersImport(list, nil, false)
		} else {
			err = prepareUsersImport(list, oldUsers, overwrite)
		}
		if err != nil {
			return err
		}

		if replace {
			// The store does not delete the last administrator. The loop
			// below would delete the users before that one and stop, so
			// the command refuses here, before the backup and the first
			// deletion.
			for _, user := range oldUsers {
				if user.Perm.Admin {
					return fmt.Errorf("can't replace the users: %q is an administrator, and the last "+
						"administrator cannot be deleted. Nothing was deleted or imported. "+
						"Use --overwrite, or remove users with \"users rm\"", user.Username)
				}
			}

			// Back up the users that are about to be deleted, not the ones
			// being imported. The file holds their password hashes.
			err = marshalPrivate("users.backup.json", oldUsers)
			if err != nil {
				return err
			}

			for _, user := range oldUsers {
				err = st.Users.Delete(user.ID)
				if err != nil {
					return err
				}
			}
		}

		for _, user := range list {
			err = st.Users.Save(user)
			if err != nil {
				return err
			}
		}
		return nil
	}, storeOptions{}),
}

// prepareUsersImport checks the users of a file against the registered ones,
// and decides which of them replace a registered user: those keep their ID,
// and all others lose it, so that the database gives them one.
//
// An ID is only good in the installation that gave it out. So it never
// decides alone where a user is stored: a user replaces the registered user
// that has its ID and its username, with overwrite, and is a new user
// otherwise. Stored under an ID as it stands in the file, a user would
// replace whoever has that ID here, and take over what goes by the ID, such
// as the mark that ends that user's sessions.
//
// The decision is made for the whole file against the users that were
// registered before the import. Looked up one by one while the users are
// stored, a user of the file could have the ID that the database has just
// given to an earlier one, and be taken for that one.
func prepareUsersImport(list, registered []*users.User, overwrite bool) error {
	byID := map[uint]*users.User{}
	byUsername := map[string]*users.User{}
	for _, user := range registered {
		byID[user.ID] = user
		byUsername[user.Username] = user
	}

	inFile := map[string]bool{}
	for _, user := range list {
		if inFile[user.Username] {
			return fmt.Errorf("can't import the users: the file has the user %q twice", user.Username)
		}
		inFile[user.Username] = true

		if stored, ok := byID[user.ID]; ok && overwrite && user.ID != 0 {
			if stored.Username != user.Username {
				return fmt.Errorf("can't import user %q with ID %d: here, that is the ID of the user %q. "+
					"To import %q as a new user, leave its ID out of the file or set it to 0",
					user.Username, user.ID, stored.Username, user.Username)
			}
			continue
		}

		if stored, ok := byUsername[user.Username]; ok {
			if overwrite {
				return fmt.Errorf("can't import user %q: the username is already registered, with the ID %d, "+
					"and --overwrite replaces a user only when the file has that ID for it",
					user.Username, stored.ID)
			}
			return fmt.Errorf("can't import user %q: the username is already registered. "+
				"With --overwrite, a user that has the ID %d in the file replaces the registered one",
				user.Username, stored.ID)
		}

		user.ID = 0
	}

	return nil
}
