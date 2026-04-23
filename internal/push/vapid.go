package push

import (
	"context"
	"fmt"

	webpush "github.com/SherClockHolmes/webpush-go"
)

// LoadOrGenerateVAPID returns the persisted VAPID keypair, generating
// and persisting a fresh one on first run. Env-supplied values (if both
// publicKey and privateKey are non-empty) always win and are written
// back into the store so subsequent restarts stay stable if the env
// var is removed.
//
// subject is the "Subscriber" claim embedded in VAPID-signed requests.
// Push services use it to reach out to the server owner if something
// misbehaves; mailto: URLs are the canonical form but any URL works.
func LoadOrGenerateVAPID(ctx context.Context, store *Store, envPub, envPriv, subject string) (VAPID, error) {
	if subject == "" {
		// Apple's web.push.apple.com rejects the JWT with BadJwtToken
		// when the "sub" claim uses a reserved / undeliverable address
		// like @example.com or @localhost. Default to a mailto: URI
		// using a domain nobody routes mail for, but that still parses
		// as a valid RFC-5322 address; self-hosters are expected to set
		// VAPID_SUBJECT to their own mailto: or https: origin (iPhone
		// users: this is required — see docs/INSTALL.md).
		subject = "mailto:rivolt@invalid"
	}
	// Operator-supplied env vars take precedence and get persisted.
	if envPub != "" && envPriv != "" {
		v := VAPID{PublicKey: envPub, PrivateKey: envPriv, Subject: subject}
		if err := store.SaveVAPID(ctx, v); err != nil {
			return VAPID{}, fmt.Errorf("persist env VAPID: %w", err)
		}
		return v, nil
	}
	if existing, err := store.GetVAPID(ctx); err != nil {
		return VAPID{}, fmt.Errorf("load VAPID: %w", err)
	} else if existing != nil {
		// Keep a stored keypair as-is, but allow subject to be updated
		// from env without rotating the keys — that would invalidate
		// every existing browser subscription.
		if existing.Subject != subject {
			existing.Subject = subject
			_ = store.SaveVAPID(ctx, *existing)
		}
		return *existing, nil
	}
	priv, pub, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		return VAPID{}, fmt.Errorf("generate VAPID: %w", err)
	}
	v := VAPID{PublicKey: pub, PrivateKey: priv, Subject: subject}
	if err := store.SaveVAPID(ctx, v); err != nil {
		return VAPID{}, fmt.Errorf("persist VAPID: %w", err)
	}
	return v, nil
}
