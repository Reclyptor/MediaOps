// Command mkv-order checks each disc's episode order against the disc's own
// Play-All, reading an mkv-probe inventory.
package main

import (
	"github.com/reclyptor/mediaops/internal/cli"
	"github.com/reclyptor/mediaops/internal/inventory"
)

func main() {
	cli.Main("mkv-order", inventory.OrderCommand)
}
