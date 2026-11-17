# filebrowser health

Check that a running File Browser answers

## Synopsis

Requests /health from a running File Browser and fails when the
answer is not 200 OK with the status "OK" in it. It is meant for health
checks, such as the one of the Docker image, where there is no shell and no
HTTP client to do it with. It gives up after 2 seconds.

Where the server listens is looked up in this order:

- The flags below, and "--config". When any of them is given, nothing else
  than the flags, the FB_ environment variables and the configuration file
  is used, as the server itself reads them.
- The file that the FB_HEALTH_FILE environment variable names. When the
  variable is set, the server writes there where it listens once it has
  started, and removes the file when it stops. That is where it really
  listens, whether that came from its flags, the environment, the
  configuration file or the database. The Docker image sets the variable.
- The FB_ environment variables and the configuration file.

The database is not read, because the running server holds it locked. So
without FB_HEALTH_FILE, a value that only the database has, or that was given
to the server as a flag, has to be given to this command as a flag too.

```
filebrowser health [flags]
```

## Options

```
  -a, --address string      address to listen on (default "127.0.0.1")
  -b, --baseURL string      base url
  -t, --cert string         tls certificate
  -h, --help[=true|false]   help for health
  -k, --key string          tls key
  -p, --port string         port to listen on (default "8080")
      --socket string       socket to listen to (cannot be used with address, port, cert nor key flags)
```

## Options inherited from parent commands

```
  -c, --config string     config file path
  -d, --database string   database path (default "./filebrowser.db")
```

## See Also

* [filebrowser](filebrowser.md)	 - A stylish web-based file browser

