package main

import (
	forgecmd "github.com/initializ/forge/forge-cli/cmd"
)

var (
	version = "dev"
	commit  = "none"
)

func main() {
	forgecmd.SetVersionInfo(version, commit)
	forgecmd.Execute()
}
