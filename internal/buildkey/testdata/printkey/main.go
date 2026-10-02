// Command printkey prints the server key and app ID linked into a binary, so
// the buildkey test can check that its linker flags really set them.
package main

import (
	"fmt"

	"github.com/Fever-r/BombeCam/pkg/bridge"
	"github.com/Fever-r/BombeCam/pkg/serverkey"
)

func main() {
	fmt.Println(serverkey.LatestKnown + " " + bridge.LatestKnownAppID)
}
