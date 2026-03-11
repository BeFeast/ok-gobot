package version

import "fmt"

var (
	Version = "dev"
	Commit  = "unknown"
)

func String() string {
	return fmt.Sprintf("%s (%s)", Version, Commit)
}
