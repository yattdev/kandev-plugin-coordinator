package main

import (
	"github.com/kandev/kandev/pkg/pluginsdk"

	"kandev-plugin-coordinator/server/coordinator"
)

func main() {
	plugin := coordinator.New()
	defer plugin.Close()
	pluginsdk.Serve(plugin)
}
