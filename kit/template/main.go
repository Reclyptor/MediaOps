// A starting point for a one-off analysis program. Copy this directory, replace
// the body of main with the job, and `go mod tidy && go run .`. It builds with no
// network: kit and its dependencies are in the image.
package main

import (
	"fmt"

	"github.com/reclyptor/mediaops/kit/paths"
)

func main() {
	cfg := paths.FromEnv()
	fmt.Println("media root:", cfg.MediaRoot)
	fmt.Println("staging:   ", cfg.Staging())
}
