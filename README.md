# maddy

[![builds.sr.ht status](https://builds.sr.ht/~emersion/maddy.svg)](https://builds.sr.ht/~emersion/maddy?)

Simple, fast, secure all-in-one mail server.

**⚠️ Disclaimer: maddy is in early development, many planned features are
missing and bugs are waiting to eat your messages**

Join [##maddy on irc.freenode.net](https://webchat.freenode.net/##maddy), if you
have any questions or just want to talk about maddy.

## Table of contents

- [Features](#features)
- [Installation](#installation)
- [Building from source](#building-from-source)
  - [Dependencies](#dependencies)
  - [Building](#building)
- [Quick start](#quick-start)
  - [systemd unit](#systemd-unit)
- [SQL-based database](#sql-based-database)
  - [PostgreSQL instead of SQLite](#postgresql-instead-of-sqlite)
- [System authentication helper binaries](#system-authentication-helper-binaries)
- [Documentation](#documentation)
- [Contributing](#contributing)
- [License](#license)

## Features

- Single binary for easy deployment
- Minimal configuration changes required to get almost complete email stack running
- [MTA-STS](https://www.hardenize.com/blog/mta-sts) support
- DNS sanity checks for incoming deliveries
- Built-in [DKIM](https://blog.returnpath.com/how-to-explain-dkim-in-plain-english-2/) verification support
- [Subaddressing](https://en.wikipedia.org/wiki/Email_address#Sub-addressing)
  (aka plus-addressing) support

Planned features:
- Built-in [DKIM](https://blog.returnpath.com/how-to-explain-dkim-in-plain-english-2/) signing
- [DMRAC](https://blog.returnpath.com/how-to-explain-dmarc-in-plain-english/) policy support
- Built-in [backscatter](https://en.wikipedia.org/wiki/Backscatter_(e-mail)) mitigation
- Address aliases support
- DANE support
- Storage encryption
- Automatic TLS certificates configuration using Let's Encrypt
- [JMAP](https://jmap.io)

## Installation

Pre-built binaries for releases are available
[here](https://github.com/foxcpp/maddy/releases).

## Building from source

#### Dependencies 

- [Go](https://golang.org) toolchain (1.13 or newer)
  
  If your distribution ships an outdated Go version, you can use
  following commands to get a newer version:
  ```
  go get golang.org/dl/go1.13
  go1.13 download
  ```
  
  Obviously, you need to have $GOPATH/bin ($HOME/go/bin by default) in $PATH.
  Then use `go1.13` instead of `go` in commands below.
  
- C compiler (**optional**, set CGO_ENABLED env. variable to 0 to disable)

  Required for SQLite3-based storage (default configuration) and PAM
  authentication. SQLite3 support can be disabled using nosqlite3 build tag:
  ```
  GO111MODULE=on go get github.com/foxcpp/maddy/cmd/maddy@master -tags 'nosqlite3' 
  ```
 
- libpam (**optional**, not needed by default)

  Used for PAM auth helper binary or direct PAM use, later is disabled by
  default and can be enabled using 'libpam' build tag
  (e.g. `go get ... -tags 'libpam nosqlite3'`)
  
**Note:** Main executable built with CGO_ENABLED=0 does not depend on any system
library and can be moved freely between systems. Executable built without libpam
but with CGO_ENABLED=1 (default) depends only on libc. Executable built with
libpam depends on system libpam, obviously.

### Building

First, make sure Go Modules support is enabled:
```
export GO111MODULE=on
```

Command to build latest commit from the master branch:
```
go get github.com/foxcpp/maddy/cmd/maddy@master
```

Command to build release X.Y.Z:
```
go get github.com/foxcpp/maddy/cmd/maddy@vX.Y.Z
```

There is no need to clone this repo, `go get` command will take care of it.

`maddy` executable  will be placed in $GOPATH/bin directory (defaults to
$HOME/go/bin).

## Quick start

You need to make sure configuration is placed in /etc/maddy/maddy.conf (copy
maddy.conf from this repo) and also /var/lib/maddy and /run/maddy exist and
writable.

Then, simply run the executable:
```
maddy 
```

You can specify custom locations for all directories and config file, so here is
no need to mess with directories in your system:
```
maddy -config /maddy.conf -state /state -runtime /tmp 
```
maddy will create specified directories for you.

### systemd unit

Alternatively, you can use the systemd unit file from [dist/](dist) directory in
this repo. It will automatically set-up user account and directories.
Additionally, it will apply strict sandbox to maddy to ensure additional
security.

You need a relatively new systemd version (235+) both of these things to work
properly. Otherwise, you still have to manually create a user account and the
state + runtime directories with read-write permissions for the maddy user. 

## Configuration

Start by copying contents of the [maddy.conf](maddy.conf) to
`/etc/maddy/maddy.conf` (default configuration location).

You need to change the following directives to your values:
- `tls cert_file key_file` 
  Change to paths to TLS certificate and key.
- `hostname`
  Server identifier. Put your domain here if you have only one server.
- `$(primary_domain)`
  Put the "main" domain you are handling messages for here.
- `$(local_domains)`
  If you have additional domains you want to accept mail for - put them here.

With that configuration you will get the following:
- SQLite-based storage for messages
- Authentication using SQLite-based virtual users DB
  (see [maddyctl utility](#maddyctl-utility))
- SMTP endpoint for incoming messages on 25 port.
- SMTP Submission endpoint for messages from your users, on both 587 (STARTTLS) 
and 465 (TLS) ports.
- IMAP endpoint for access to user mailboxes, on both 143 (STARTTLS) and 
993 (TLS) ports.
- Two basic DNS checks for incoming messages
- DKIM signatures verification
- Subaddressing (aka plus-addressing) support

Configuration syntax, high-level structure and base components are documented 
in [maddy.conf(5)](man/maddy.conf.5.scd) man page. Other modules are described
in corresponding man pages.

### Mailboxes namespacing

By default, all configured domains will share the same set of mailboxes.
This means, if you use
``` 
$(local_domains) = example.com example.org
```
admin@example.com and admin@example.org will refer to the same mailbox.

If you want to make them separate - add `auth_perdomain` and `storage_perdomain`
directives below.

Note that it will require users to specify the full address as a username when
logging in.

## maddyctl utility

To manage virtual users, mailboxes and messages maddyctl utility
should be used. It can be installed using the following command:
```
go get github.com/foxcpp/maddy/cmd/maddyctl@master
```
**Note:** Use the same version as maddy, e.g. if you installed maddy X.Y.Z, 
then use the following command:
```
go get github.com/foxcpp/maddy/cmd/maddyctl@vX.Y.Z
```

As with any other `go get` command, binary will be placed in `$GOPATH/bin`
($HOME/go/bin by default).


Here is the command to create virtual user account:
```
maddyctl users create foxcpp
```

It assumes you use default locations for state directory and config file.
If that's not the case, provide `-config` and/or `-state` arguments 
*before "users" subcommand* that specify used values.


### PostgreSQL instead of SQLite

If you want to use PostgreSQL instead of SQLite 3 (e.g. you want better access
concurrency), here is what you need to do:

1. Install, configure and start PostgreSQL server

There is plenty of tutorials on the web. Note that you need PostgreSQL 9.6 or newer.

2. Create the database and user role for maddy

```
psql -U postgres -c 'CREATE USER maddy'
psql -U postgres -c 'CREATE DATABASE maddy'
```

3. Update maddy.conf to use PostgreSQL and that database

```
sql {
    driver postgres
    dsn user=maddy dbname=maddy sslmode=disable
}
```

Add `host=` option is server is running on the different machine 
(configure TLS and remove `sslmode=disable` then!).

See https://godoc.org/github.com/lib/pq for driver options documentation.

## Documentation

Reference documentation is maintained as a set of man pages
in the [scdoc](https://git.sr.ht/~sircmpwn/scdoc) format.
Rendered pages can be browsed [here](https://foxcpp.dev/maddy-reference).

Tutorials and misc articles will be added to
the [project wiki](https://github.com/foxcpp/maddy/wiki).

## System authentication helper binaries

By default maddy is running as an unprivileged user, meaning it can't read
system account passwords. There are two options:

##### Run maddy as a privileged user instead (not recommended):

This way you can use maddy without messing with helper binaries, but that also
gives maddy too many permissions.
  
##### Let maddy start a privileged helper binary to perform authentication:

1. Compile [cmd/maddy-pam-helper](cmd/maddy-pam-helper) and/or
   [cmd/maddy-shadow-helper](cmd/maddy-shadow-helper).
2. Put them into `/usr/lib/maddy` and make them setuid root (there are also
   other more restrictive options, check README in the executable directories).
3. Add `use_helper` to `pam` or `shadow` configuration block in your config.
4. Modify systemd unit to use less strict sandbox and disable DynamicUser.

## Contributing

See [.github/CONTRIBUTING.md](.github/CONTRIBUTING.md).

## License

The code is under MIT license. See [LICENSE](LICENSE) for more information.
