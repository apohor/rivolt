package secrets

import (
	"context"
	"sync/atomic"
)

type decryptSiteKey struct{}

// WithSite labels the decrypts of a stored Rivian session made under ctx with the place that asked (a value of metrics.SessionDecryptSites;
// the observer maps anything else to "other"). It names a code site, never a user.
func WithSite(ctx context.Context, site string) context.Context {
	return context.WithValue(ctx, decryptSiteKey{}, site)
}

var decryptObserver atomic.Pointer[func(site string)]

// SetSessionDecryptObserver installs the function LoadRivianSession calls after each successful decrypt with the context's site ("" when
// the caller set none). Nil removes it.
func SetSessionDecryptObserver(fn func(site string)) {
	if fn == nil {
		decryptObserver.Store(nil)
		return
	}
	decryptObserver.Store(&fn)
}

func observeSessionDecrypt(ctx context.Context) {
	if p := decryptObserver.Load(); p != nil {
		site, _ := ctx.Value(decryptSiteKey{}).(string)
		(*p)(site)
	}
}
