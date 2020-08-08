#!/usr/bin/env bash

echo '!!! build.sh script is deprecated and will be removed in the next release.'

options=$(getopt -o hb:p:d: -l help,builddir:,prefix:,destdir:,systemddir:,configdir:,statedir:,runtimedir:,fail2bandir:,tags:,prefix:,gitversion:,version:,source:,sudo -- "$@")
eval set -- "$options"
print_help() {
    cat >&2 <<EOF
Usage:
    ./build.sh [options] [subroutines]

Script to compile and install maddy mail server.

Options:
    -h, --help                guess!
    -p, --prefix <path>       installation prefix (default: /usr/local, \$PREFIX)
    -d, --destdir <path>      prefix all paths with this directory during installation
                               (default: empty string, \$DESTDIR)
    -b, --builddir <path>     directory to use to store build files
                               (default: \$PWD/build, \$BUILDDIR)
    --systemddir <path>       directory to install systemd units to
                               (default: \$PREFIX/lib/systemd, \$SYSTEMDUNITS)
    --configdir <path>        directory to install configuration files to
                               (default: /etc/maddy/, \$CONFDIR)
    --statedir <path>         default state directory location
                               (default: /var/lib/maddy, \$STATEDIR)
    --runtimedir <path>       default runtime directory location
                               (default: /run/maddy, \$RUNTIMEDIR)
    --fail2bandir <path>      directory to install fail2ban configuration to
                               (default: /etc/fail2ban, \$FAIL2BANDIR)
    --tags <string>           Go build tags to use
    --gitversion <revision>   git commit or tag to checkout if not building inside
                               existing tree (default: master, \$GITVERSION)
    --version <revision>      bypass Git tag version detection and use specified string
                               (default: unknown, \$MADDY_VER)
    --source <path>           path to the maddy source tree to use
                               (default: look for go.mod or download, \$MADDY_SRC)
    --sudo                    run install_pkg and create_user with sudo

If no [subroutines] are specified, package, create_user, install_pkg are used with
latter two being executed using sudo.

Otherwise specified subroutines are executed in the specified order.

Available subroutines:
- package
  Download sources, compile binaries and install everything under correspoding
  directories in \$BUILDDIR/pkg.
- create_user
  Create user and group named 'maddy'.
- install_pkg
  Copy contents of \$BUILDDIR/pkg to \$DESTDIR.  Special case is maddy.conf
  file, if it exists in \$DESTDIR, it is copied to maddy.conf.new instead of
  overwritting. package subroutine should executed before for this to work
  properly.
- ensure_go
  Check system Go toolchain for compatibility and download a newer version from
  golang.org if necessary.
- ensure_source_tree
  Download source code if necessary or use current directory if it is a Go
  module tree already.
- compile_binaries
  Build executable files and copy them to \$BUILDDIR/pkg/\$PREFIX/bin.
- build_man_pages
  Build manual pages and copy them to \$BUILDDIR/pkg/\$PREFIX/man.
- prepare_cfg
  Copy default config to \$BUILDDIR/pkg.
- prepare_misc
  Copy contents of dist/ to \$BUILDDIR/pkg, patching paths to match \$PREFIX if
  necessary.

Examples:
    ./build.sh
        Compile maddy and install it along with all necessary stuff (e.g.
        systemd units).
    ./build.sh --gitversion=47777793ed0
        Same as above but build specific Git commit.
    ./build.sh package
        Compile maddy, but do not install anything.
    ./build.sh compile_binaries install_pkg
        Compile and install only executables without any extra stuff like
        fail2ban configs.
EOF
}

read_config() {
    # This will make sure the variable safe to use after 'set -u'
    # if it is not defined, the empty value is fine.
    export DESTDIR=$DESTDIR

    export TAGS=""

    # Most variables are exported so they are accessible when we call "$0" with
    # sudo.

    if [ -z "$GITVERSION" ]; then
        export GITVERSION=master
    fi
    if [ -z "$PREFIX" ]; then
        export PREFIX=/usr/local
    fi
    if [ -z "$CONFDIR" ]; then
        export CONFDIR=/etc/maddy
    fi
    if [ -z "$STATEDIR" ]; then
        export STATEDIR=/var/lib/maddy
    fi
    if [ -z "$RUNTIMEDIR" ]; then
        export RUNTIMEDIR=/run/maddy
    fi
    if [ -z "$FAIL2BANDIR" ]; then
        export FAIL2BANDIR=/etc/fail2ban
    fi
    if [ -z "$BUILDDIR" ]; then
        export BUILDDIR="$PWD/build"
    fi
    if [ -z "$MADDY_VER" ]; then
        export MADDY_VER=unknown
    fi
    if [ -z "$MADDY_SRC" ]; then
        export MADDY_SRC=
    fi

    export CGO_CFLAGS="-g -O2 -D_FORTIFY_SOURCE=2 $CFLAGS"
    export CGO_CXXFLAGS="-g -O2 -D_FORTIFY_SOURCE=2 $CXXFLAGS"
    export LDFLAGS="-Wl,-z,relro,-z,now $LDFLAGS"
    export CGO_LDFLAGS=$LDFLAGS

    while true; do
        case "$1" in
            -h|--help)
               print_help
               exit
               ;;
            -b|--builddir)
               shift
               export BUILDDIR="$1"
               ;;
            -p|--prefix)
               shift
               export PREFIX="$1"
               ;;
            -d|--destdir)
                shift
                export DESTDIR="$1"
                ;;
            --systemddir)
                shift
                export SYSTEMDUNITS="$1"
                ;;
            --configdir)
                shift
                export CONFDIR="$1"
                ;;
            --statedir)
                shift
                export STATEDIR="$1"
                ;;
            --runtimedir)
                shift
                export RUNTIMEDIR="$1"
                ;;
            --fail2bandir)
                shift
                export FAIL2BANDIR="$1"
                ;;
            --tags)
                shift
                export TAGS="$1"
                ;;
            --gitversion)
                shift
                export GITVERSION="$1"
                ;;
            --version)
                shift
                export MADDY_VER="$1"
                ;;
            --source)
                shift
                export MADDY_SRC="$1"
                ;;
            --sudo)
                export elevate=1
                ;;
            --)
                break
                shift
                ;;
            *)
                echo "Unknown option: ${arg}. See --help." >&2
                exit 2
        esac
        shift
    done

    # Since this variable depends on $PREFIX, read it after processing command
    # line arguments.
    if [ -z "$SYSTEMDUNITS" ]; then
        export SYSTEMDUNITS=$PREFIX/lib/systemd
    fi

    shift
    positional=( "${@}" )
}


# Test whether the Go toolchain is available on the system and matches required
# version. If it is not present or incompatible - download Go $GOVERSION and unpack.
ensure_go() {
    REQUIRED_GOVERSION=1.14.0
    GOVERSION=1.14.3

    pushd "$BUILDDIR" >/dev/null

    if ! command -v go >/dev/null; then
        downloadgo=1
    else
        SYSGOVERSION=$(go version | cut -f3 -d ' ' | grep -Eo "([0-9]+\.?)+")
        SYSGOMAJOR=$(cut -f1 -d. <<<"$SYSGOVERSION")
        SYSGOMINOR=$(cut -f2 -d. <<<"$SYSGOVERSION")
        SYSGOPATCH=$(cut -f3 -d. <<<"$SYSGOVERSION")
        if [ "$SYSGOPATCH" == "" ]; then
            SYSGOPATCH=0
        fi
        WANTEDGOMAJOR=$(cut -f1 -d. <<<$REQUIRED_GOVERSION)
        WANTEDGOMINOR=$(cut -f2 -d. <<<$REQUIRED_GOVERSION)
        WANTEDGOPATCH=$(cut -f3 -d. <<<$REQUIRED_GOVERSION)

        downloadgo=0
        if [ "$SYSGOMAJOR" -ne "$WANTEDGOMAJOR" ]; then
            downloadgo=1
        fi
        if [ "$SYSGOMINOR" -lt "$WANTEDGOMINOR" ]; then
            downloadgo=1
        fi
        if [ "$SYSGOPATCH" -lt "$WANTEDGOPATCH" ]; then
            downloadgo=1
        fi

        if [ $downloadgo -eq 0 ]; then
            echo "--- Using system Go toolchain ($SYSGOVERSION, $(command -v go))." >&2
        fi
    fi

    if [ $downloadgo -eq 1 ]; then
        echo "--- Downloading Go $GOVERSION toolchain..." >&2
        if ! [ -e go$GOVERSION ]; then
            if ! [ -e go$GOVERSION.linux-amd64.tar.gz ]; then
                curl "https://dl.google.com/go/go$GOVERSION.linux-amd64.tar.gz" -o go$GOVERSION.linux-amd64.tar.gz
            fi
            tar xf go$GOVERSION.linux-amd64.tar.gz
            mv go go$GOVERSION
        fi
        export GOROOT="$PWD/go$GOVERSION"
        export PATH=$PWD/go$GOVERSION/bin:$PATH

        echo "--- Using downloaded Go toolchain ($GOVERSION, $(command -v go))." >&2
    fi

    popd >/dev/null
}

# If the script is executed independently (e.g. via curl|bash) - download the
# maddy source code.
ensure_source_tree() {
    # Use specified source tree instead of auto-detection.
    if [ -n "$MADDY_SRC" ]; then
        echo '--- Using existing source tree...' >&2
        return
    fi

    gomod="$(go env GOMOD)"
    downloaded=0
    # /dev/null is used when Go module mode is forced, otherwise it is just an
    # empty string. Check both to avoid depending on environment.
    if [ "$gomod" = "/dev/null" ] || [ "$gomod" = "" ]; then
        echo '--- Downloading source tree...' >&2
        if [ ! -e "$BUILDDIR/maddy" ]; then
            git clone https://github.com/foxcpp/maddy.git "$BUILDDIR/maddy"
        fi
        export MADDY_SRC="$BUILDDIR/maddy"
        pushd "$MADDY_SRC" >/dev/null
        git stash push --quiet --all
        git fetch origin master

        if [ "$GITVERSION" != "" ]; then
            git checkout --quiet "$GITVERSION"
        fi
        downloaded=1
    else
        MADDY_SRC="$(dirname "$gomod")"
        export MADDY_SRC
        pushd "$MADDY_SRC" >/dev/null
    fi

    if [ ! -e "$MADDY_SRC/.git" ]; then
        if [ "$MADDY_VER" == "unknown" ]; then
            echo '--- WARNING: Source tree is not a Git repository and no version specified.' >&2
        fi
        popd >/dev/null
        return
    fi

    # Turn vVERSION-COMMITS-HASH into VERSION-devCOMMITS+HASH with minor
    # component of VERSION increased by one if the amount of commits since last
    # tag is more than one.
    set +e
    DESCR=$(git describe --long 2>/dev/null)
    if [ $? -ne 0 ]; then
        if [ "$MADDY_VER" == "unknown" ]; then
            echo '--- WARNING: No Git tags found and no version specified.' >&2
        fi
        popd >/dev/null
        return
    fi
    set -e

    MADDY_MAJOR=$(sed 's/^v//' <<<$DESCR | cut -f1 -d '.')
    MADDY_MINOR=$(cut -f2 -d '.' <<<$DESCR )

    if [ "$GITVERSION" == "master" ] && [ "$downloaded" -eq 1 ]; then
        set +e
        if git branch -r | grep "origin/$MADDY_MAJOR.$MADDY_MINOR-fixes" >/dev/null; then
            echo "--- Using $MADDY_MAJOR.$MADDY_MINOR-fixes tree" >&2
            git checkout "$MADDY_MAJOR.$MADDY_MINOR-fixes"
            DESCR=$(git describe --long 2>/dev/null)
        fi
        set -e
    fi

    MADDY_PATCH=$(cut -f1 -d '-' <<<$DESCR | sed 's/-.+//' | cut -f3 -d '.')
    MADDY_SNAPSHOT=$(cut -f2 -d '-' <<<$DESCR)
    MADDY_COMMIT=$(cut -f3 -d '-' <<<$DESCR)

    if [ $MADDY_SNAPSHOT -ne 0 ]; then
        (( MADDY_MINOR++ ))
        MADDY_PATCH=0

        MADDY_VER="$MADDY_MAJOR.$MADDY_MINOR.$MADDY_PATCH-dev$MADDY_SNAPSHOT+$MADDY_COMMIT"
    else
        MADDY_VER="$MADDY_MAJOR.$MADDY_MINOR.$MADDY_PATCH+$MADDY_COMMIT"
    fi
    export MADDY_VER

    echo "--- maddy $MADDY_VER" >&2

    popd >/dev/null
}

compile_binaries() {
    mkdir -p "$PKGDIR/$PREFIX/bin/"
    pushd "$MADDY_SRC" >/dev/null

    echo '--- Downloading dependencies...' >&2
    go get -d ./...

    echo '--- Building main executable...' >&2
    go build -trimpath -buildmode=pie -tags "$TAGS" \
        -ldflags "-extldflags \"$LDFLAGS\" \
            -X \"github.com/foxcpp/maddy.DefaultLibexecDirectory=$PREFIX/lib/maddy\" \
            -X \"github.com/foxcpp/maddy.DefaultStateDirectory=$STATEDIR\" \
            -X \"github.com/foxcpp/maddy.DefaultRuntimeDirectory=$RUNTIMEDIR\" \
            -X \"github.com/foxcpp/maddy.ConfigDirectory=$CONFDIR\" \
            -X \"github.com/foxcpp/maddy.Version=$MADDY_VER\"" \
        -o "$PKGDIR/$PREFIX/bin/maddy" ./cmd/maddy

    echo '--- Building management utility executable...' >&2
    go build -trimpath -buildmode=pie -tags "$TAGS" \
        -ldflags "-extldflags \"$LDFLAGS\" \
            -X \"github.com/foxcpp/maddy.DefaultLibexecDirectory=$PREFIX/lib/maddy\" \
            -X \"github.com/foxcpp/maddy.DefaultStateDirectory=$STATEDIR\" \
            -X \"github.com/foxcpp/maddy.DefaultRuntimeDirectory=$RUNTIMEDIR\" \
            -X \"github.com/foxcpp/maddy.ConfigDirectory=$CONFDIR\" \
            -X \"github.com/foxcpp/maddy.Version=$MADDY_VER\"" \
        -o "$PKGDIR/$PREFIX/bin/maddyctl" ./cmd/maddyctl

    popd >/dev/null
}

build_man_pages() {
    set +e
    if ! command -v scdoc &>/dev/null; then
        echo '--- No scdoc utility found. Skipping man pages installation.' >&2
        set -e
        return
    fi
    if ! command -v gzip &>/dev/null; then
        echo '--- No gzip utility found. Skipping man pages installation.' >&2
        set -e
        return
    fi
    set -e

    echo '--- Building man pages...' >&2

    for f in "$MADDY_SRC"/docs/man/*.1.scd; do
        scdoc < "$f" | gzip > /tmp/maddy-tmp.gz
        install -Dm 0644 /tmp/maddy-tmp.gz "$PKGDIR/$PREFIX/share/man/man1/$(basename "$f" .scd).gz"
    done
    for f in "$MADDY_SRC"/docs/man/*.5.scd; do
        scdoc < "$f" | gzip > /tmp/maddy-tmp.gz
        install -Dm 0644 /tmp/maddy-tmp.gz "$PKGDIR/$PREFIX/share/man/man5/$(basename "$f" .scd).gz"
    done
}

prepare_misc() {
    echo '--- Preparing integration files...' >&2

    pushd "$MADDY_SRC/dist" >/dev/null

    install -Dm 0644 -t "$PKGDIR/$PREFIX/share/vim/vimfiles/ftdetect/" vim/ftdetect/maddy-conf.vim
    install -Dm 0644 -t "$PKGDIR/$PREFIX/share/vim/vimfiles/ftplugin/" vim/ftplugin/maddy-conf.vim
    install -Dm 0644 -t "$PKGDIR/$PREFIX/share/vim/vimfiles/syntax/" vim/syntax/maddy-conf.vim

    install -Dm 0644 -t "$PKGDIR/$FAIL2BANDIR/jail.d/" fail2ban/jail.d/*
    install -Dm 0644 -t "$PKGDIR/$FAIL2BANDIR/filter.d/" fail2ban/filter.d/*

    install -Dm 0644 -t "$PKGDIR/$PREFIX/lib/systemd/system/" systemd/maddy.service systemd/maddy@.service

    sed -Ei "s!/usr/bin!$PREFIX/bin!g;\
        s!/usr/lib/maddy!$PREFIX/lib/maddy!g;\
        s!/etc/maddy!$CONFDIR!g" "$PKGDIR/$SYSTEMDUNITS/system/maddy.service" "$PKGDIR/$SYSTEMDUNITS/system/maddy@.service"

    popd >/dev/null
}

prepare_cfg() {
    install -Dm 0644 "$MADDY_SRC/maddy.conf" "$PKGDIR/$CONFDIR/maddy.conf"
}

check_system_deps() {
    if [ "$(go env CC)" = "" ]; then
        echo 'WARNING: No C compiler available. maddy will be built without SQLite3 support and default configuration will be unusable.' >&2
    fi
}

package() {
    ensure_go
    ensure_source_tree

    check_system_deps

    compile_binaries
    build_man_pages
    prepare_misc
    prepare_cfg
}

# Prevent 'unbound variable' in install_pkg and create_user if they are called
# directly via './build.sh ...'.
elevate=0

install_pkg() {
    echo '--- Installing built tree...' >&2
    if [ -e "$DESTDIR/$CONFDIR/maddy.conf" ]; then
        echo '--- maddy.conf exists, installing default configuration as maddy.conf.new...' >&2
        # Can be false for repeately executed install_pkg.
        if [ -e "$PKGDIR/$CONFDIR/maddy.conf" ]; then
            mv "$PKGDIR/$CONFDIR/maddy.conf"{,.new}
        fi
    fi

    pushd "$PKGDIR" >/dev/null
    while IFS= read -r -d '' f
    do
        # This loop runs in a subshell and does not inherit shell options.
        # -e is useful here to abort early if user presses Ctrl-C on sudo
        # password prompt.
        set -e

        if [ "$elevate" -eq 1 ]; then
            sudo install -Dm "$(stat -c '%a' "$f")" "$f" "$DESTDIR/$f"
        else
            install -Dm "$(stat -c '%a' "$f")" "$f" "$DESTDIR/$f"
        fi
    done <   <(find . -mindepth 1 -type f -print0)
    popd >/dev/null
}

create_user() {
    set +e
    if ! grep -q "maddy:x" /etc/passwd; then
        echo '--- Creating maddy user...' >&2
        if [ "$elevate" -eq 1 ]; then
            sudo useradd -M -U -s /usr/bin/nologin -d /var/lib/maddy/ maddy
        else
            useradd -M -U -s /usr/bin/nologin -d /var/lib/maddy/ maddy
        fi
    fi
}

start_banner() {
    cat >&2 <<EOF
                                _|        _|
_|_|_|  _|_|      _|_|_|    _|_|_|    _|_|_|  _|    _|
_|    _|    _|  _|    _|  _|    _|  _|    _|  _|    _|
_|    _|    _|  _|    _|  _|    _|  _|    _|  _|    _|
_|    _|    _|    _|_|_|    _|_|_|    _|_|_|    _|_|_|
                                                    _|
 All-in-one composable mail server.             _|_|

EOF
}

finish_banner() {
    cat >&2 <<EOF
--- Successfully installed maddy v$MADDY_VER.

Next steps:

0. Set host name and domain in $CONFDIR/maddy.conf.

1. Configure TLS certificates.
See https://foxcpp.dev/maddy/tutorials/setting-up/#tls-certificates
TL;DR Put correspoding paths in 'tls' directive in $CONFDIR/maddy.conf.

2. Start server once to generate DKIM keys.
# systemctl daemon-reload
# systemctl start maddy

3. Configure DNS records for your domain.
See https://foxcpp.dev/maddy/tutorials/setting-up/#dns-records

4. Enjoy!
EOF

}

# Checks if sudo is installed and sets the elevate variable for 
# later use in the create_user and install_pkg functions
check_if_sudo_is_installed() {
    if sudo -n true
    then
      export elevate=1
    else
      export elevate=0
    fi
}

run() {
    IFS=$'\n'
    read_config "$@"
    export PKGDIR="$BUILDDIR/pkg"
    set -euo pipefail

    mkdir -p "$BUILDDIR" "$PKGDIR"

    if [ ${#positional[@]} -ne 0 ]; then
        for arg in "${positional[@]}"; do
            eval "$arg"
        done
        exit
    fi

    # Do not print fancy banners if the script is likely running from an
    # automated package build environment.
    if [ "$DESTDIR" = "" ]; then
        start_banner
    fi

    package
    check_if_sudo_is_installed
    create_user
    install_pkg

    if [ "$DESTDIR" = "" ]; then
        finish_banner
    fi
}
run "$@"
