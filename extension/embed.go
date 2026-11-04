// Package extension holds vaultr's browser extension. The files are
// embedded in vaultr, which writes them out on `vaultr extension install`;
// see internal/browserext.
package extension

import "embed"

// Files are the extension's sources. The manifest serves both browsers;
// the installer writes a per-browser copy.
//
//go:embed manifest.json background.js fill.js popup.html popup.css popup.js icons/*.png
var Files embed.FS

// dist holds the AMO-signed Firefox add-on in release builds; the release
// workflow puts it there before building. Elsewhere it holds only a
// README.
//
//go:embed dist
var dist embed.FS

// SignedXPI returns the signed Firefox add-on built into this binary, or
// nil when there is none.
func SignedXPI() []byte {
	b, err := dist.ReadFile("dist/vaultr.xpi")
	if err != nil {
		return nil
	}
	return b
}
