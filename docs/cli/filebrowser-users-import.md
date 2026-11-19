# filebrowser users import

Import users from a file

## Synopsis

Import users from a file. The path must be for a json or yaml
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
user is replaced or deleted.

```
filebrowser users import <path> [flags]
```

## Options

```
  -h, --help[=true|false]        help for import
      --overwrite[=true|false]   replace the registered users whose ID and username are those of a user in the file
      --replace[=true|false]     replace the entire user base
```

## Options inherited from parent commands

```
  -c, --config string     config file path
  -d, --database string   database path (default "./filebrowser.db")
```

## See Also

* [filebrowser users](filebrowser-users.md)	 - Users management utility

