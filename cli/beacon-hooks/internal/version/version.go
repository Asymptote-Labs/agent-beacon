package version

// Version is set at build time via ldflags
var Version = "dev"

// Edition names the build, like the beacon CLI's version.Edition. Builds from this repository are
// "community"; a build that compiles in files of its own sets it at link time.
var Edition = "community"
