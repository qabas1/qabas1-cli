package version

// Version is the qabas CLI SemVer. Overridden via -ldflags at release build time.
var Version = "dev"

// Commit is the git SHA, if provided at build time.
var Commit = "none"
