package dashboard

import (
	"context"
	"io/fs"
	"net/http"
	"time"
)

// pageFS exposes the embedded assets/ subtree at the listener root, so the page
// is served from / rather than /assets/.
func pageFS() fs.FS {
	sub, err := fs.Sub(assets, "assets")
	if err != nil {
		// Unreachable: assets/ is embedded at compile time, so a failure here
		// would mean the binary was built without it. Serving an empty FS keeps
		// the JSON API working — an operator with a blank page and live
		// endpoints is better off than one with a dead listener.
		return emptyFS{}
	}
	return sub
}

// emptyFS is the degraded fallback for pageFS.
type emptyFS struct{}

func (emptyFS) Open(string) (fs.File, error) { return nil, fs.ErrNotExist }

// contextWithTimeout bounds a handler's work. Split out so the timeout is
// testable without an HTTP round trip.
func contextWithTimeout(r *http.Request, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), d)
}
