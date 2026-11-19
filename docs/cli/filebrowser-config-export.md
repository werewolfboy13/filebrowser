# filebrowser config export

Export the configuration to a file

## Synopsis

Export the configuration to a file. The path must be for a
json or yaml file. This exported configuration can be changed,
and imported again with 'config import' command.

The file holds the key that signs the sessions of the users, and
what the authentication method keeps secret. It is written so that
only its owner can read it, also when it replaces a file that
others could read.

```
filebrowser config export <path> [flags]
```

## Options

```
  -h, --help[=true|false]   help for export
```

## Options inherited from parent commands

```
  -c, --config string     config file path
  -d, --database string   database path (default "./filebrowser.db")
```

## See Also

* [filebrowser config](filebrowser-config.md)	 - Configuration management utility

