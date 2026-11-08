// Package forward is the single-owner forwarder (design §2/§3, M3): a request that needs Rivian from a pod that does not own
// the user's group is pushed to the owner through Redis lists and answered there, so only the owner calls Rivian.
//
// This is the M3-0 skeleton: the message shapes, the signing, the per-kind allowlist with strict validators, the Redis key
// names, the id generator, the expiry arithmetic, the metrics and the injected interfaces with their fakes. Nothing here is
// wired into the server and nothing talks to Redis or Postgres yet; the owner loop (M3-1), the requester and the kinds follow
// in later PRs, one each.
//
// Rules that hold for every later step:
//   - Tokens, passwords and cookies never ride a message. A message names a user and a group, never a credential. The `adopt`
//     kind carries only user and group.
//   - Every request and reply is signed (HMAC-SHA256, C1) with a purpose-labelled subkey of one install secret; the receiver
//     verifies the signature before anything else (before the schema, the lease or the Redis clock). The two directions use
//     different subkeys so a reply can never be replayed as a request.
//   - Time is Redis's: `expires_ms` is computed from Redis TIME and compared with Redis TIME; pods never compare wall clocks.
//   - Keys live under the app's ACL prefix `rivolt:` (the Valkey user `rivolt` has `~rivolt:*`).
//   - Metric labels carry no user, vehicle or group (/metrics is public).
package forward
