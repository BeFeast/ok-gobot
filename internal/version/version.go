package version

// Set via ldflags at build time:
//
//	go build -ldflags "-X ok-gobot/internal/version.Version=0.2.0
//	                    -X ok-gobot/internal/version.Commit=$(git rev-parse --short HEAD)"
var (
	Version = "dev"
	Commit  = ""
)

func String() string {
	if Commit != "" {
		return Version + " (" + Commit + ")"
	}
	return Version
}
