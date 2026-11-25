//go:build windows

package engine

import _ "embed"

// Payload is the zipped Windows engine (llama-server.exe and its DLLs),
// produced by build/build-engine.sh.
//
//go:embed payload/engine-win-x64.zip
var Payload []byte
