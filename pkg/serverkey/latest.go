package serverkey

import "github.com/Fever-r/BombeCam/internal/obscure"

// builtIn is the Osaio server key built into this binary, obscured. It is not
// in the source code: builds set it from osaio-setup.txt or
// BOMBECAM_SERVER_KEY (go run ./tools/setup writes the file;
// internal/buildkey passes it to the linker), release builds from a GitHub
// Actions secret. A build without a key leaves it empty.
var builtIn string

// LatestKnown is the built-in key in plain form: the last source BombeCam
// tries, so a release download works without setup. It is empty in a build
// without a key, and the web page then asks for one.
//
// When Osaio changes its key, users paste the new one under Settings > Server
// key, which takes effect at once and overrides this value. The maintainer
// updates the secret and releases a patch version (see
// docs/technical/MAINTAINERS.md).
var LatestKnown = obscure.Reveal("server_key", builtIn)
