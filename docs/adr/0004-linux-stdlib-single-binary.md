# One Linux binary, standard library only

ctg is one static Go binary for linux/amd64 and linux/arm64, with cgo off and no third-party modules. It depends on systemd, /proc, and SO_PEERCRED, so there is no macOS build.
