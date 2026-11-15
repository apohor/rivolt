package forward_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/redis/go-redis/v9"

	"github.com/apohor/rivolt/internal/forward"
)

// A made-up place and point: nothing real, and distinctive enough that any copy of it in Redis would be found.
const sealSentinel = "Zzyzx Test Plaza 41.2718 -96.3314"

func sealBody() json.RawMessage {
	b, _ := json.Marshal(map[string]any{"active": true, "name": sealSentinel, "lat": 41.2718, "lon": -96.3314})
	return b
}

func TestSealOpenBindsRequestKindAndSubject(t *testing.T) {
	s, _ := forward.NewSigner(bytes.Repeat([]byte{7}, forward.KeyLen))
	plain := sealBody()
	sealed, err := s.Seal("req-1", forward.KindLiveDestination, "v-1", plain)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(sealed), "Zzyzx") || strings.Contains(string(sealed), "41.27") {
		t.Fatalf("sealed body shows the plaintext: %s", sealed)
	}
	if got, err := s.Open("req-1", forward.KindLiveDestination, "v-1", sealed); err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("Open = %q, %v; want the plaintext", got, err)
	}
	for name, c := range map[string]struct {
		id, subject string
		kind        forward.Kind
	}{
		"another request":    {"req-2", "v-1", forward.KindLiveDestination},
		"another vehicle":    {"req-1", "v-2", forward.KindLiveDestination},
		"another kind":       {"req-1", "v-1", forward.KindLiveSession},
		"empty subject":      {"req-1", "", forward.KindLiveDestination},
		"id and subject run": {"req-1v", "-1", forward.KindLiveDestination},
	} {
		if _, err := s.Open(c.id, c.kind, c.subject, sealed); err == nil {
			t.Errorf("%s: a sealed body opened for the wrong binding", name)
		}
	}
	other, _ := forward.NewSigner(bytes.Repeat([]byte{8}, forward.KeyLen))
	if _, err := other.Open("req-1", forward.KindLiveDestination, "v-1", sealed); err == nil {
		t.Error("a sealed body opened under another install secret")
	}
	var b64 string
	_ = json.Unmarshal(sealed, &b64)
	tampered, _ := json.Marshal(b64[:len(b64)-4] + "AAAA")
	for name, bad := range map[string]json.RawMessage{"tampered": tampered, "not a string": json.RawMessage(`{"a":1}`), "empty string": json.RawMessage(`""`), "short": json.RawMessage(`"AAAA"`)} {
		if _, err := s.Open("req-1", forward.KindLiveDestination, "v-1", bad); err == nil {
			t.Errorf("%s: opened", name)
		}
	}
	// A random nonce: sealing twice gives two different bodies.
	again, _ := s.Seal("req-1", forward.KindLiveDestination, "v-1", plain)
	if bytes.Equal(sealed, again) {
		t.Error("two seals of the same body are identical (nonce reused)")
	}
	// The seal key is its own subkey: a signature over the same bytes is not a seal, and a request signature is unchanged by the new key.
	req := &forward.Request{V: forward.Version, ID: "x", Kind: forward.KindFreshState, Args: json.RawMessage(`{}`)}
	s.SignRequest(req)
	if s.VerifyRequest(req) != nil {
		t.Error("signing broke")
	}
}

// What the owner writes to Redis for a sealed kind is ciphertext: the reply a requester pops and the bytes anyone watching Redis could read.
func TestOwnerSealsALiveDestinationReplyInRedis(t *testing.T) {
	r := newRig(t, nil)
	r.runner.Reply = forward.Result{Status: 200, Body: sealBody()}
	r.start()
	req := r.push(forward.KindLiveDestination, `{"vehicle_id":"v-1"}`, nil)
	rep := r.reply(req)
	if rep == nil || !rep.OK || rep.Status != 200 {
		t.Fatalf("reply = %+v", rep)
	}
	raw, _ := json.Marshal(rep)
	if strings.Contains(string(raw), "Zzyzx") || strings.Contains(string(raw), "41.27") || strings.Contains(string(raw), "96.33") {
		t.Fatalf("the reply in Redis shows the destination: %s", raw)
	}
	if _, err := r.signer.Open(req.ID, forward.KindLiveDestination, "v-1", rep.Body); err != nil {
		t.Fatalf("the requester cannot open it: %v", err)
	}
	if _, err := r.signer.Open(req.ID, forward.KindLiveDestination, "v-2", rep.Body); err == nil {
		t.Error("the reply opens for another vehicle")
	}
	// Nothing else in Redis holds it either.
	assertNoSentinel(t, r.mr.Keys(), func(k string) string { return redisDump(r, k) })
}

// The whole path: a requester on another pod gets the plaintext, and while the call ran and after it no key in Redis held it.
func TestRequesterOpensASealedLiveDestinationAndRedisNeverHoldsIt(t *testing.T) {
	f := newFleet(t)
	f.owner("pod-a", 7, true, nil)
	f.runners["pod-a"].Reply = forward.Result{Status: 200, Body: sealBody()}
	call := forward.Call{Kind: forward.KindLiveDestination, User: user, Group: group, Args: forward.LiveDestinationArgs{VehicleID: "v-1"}}
	var local localCounter
	res, outcome, err := f.requester(nil).Do(context.Background(), call, local.fn)
	if err != nil || outcome != "ok" || res.Status != 200 || local.n.Load() != 0 {
		t.Fatalf("outcome=%q res=%+v err=%v local=%d", outcome, res, err, local.n.Load())
	}
	if !bytes.Equal(res.Body, sealBody()) {
		t.Fatalf("the requester got %s, want the owner's plaintext", res.Body)
	}
	for _, k := range f.mr.Keys() {
		if strings.Contains(redisDumpMR(f, k), "Zzyzx") {
			t.Fatalf("key %s holds the destination", k)
		}
	}
}

func redisDump(r *rig, k string) string { return dumpKey(r.rdb, k) }

func redisDumpMR(f *fleet, k string) string { return dumpKey(f.rdb, k) }

// dumpKey is everything a key holds, whatever its type.
func dumpKey(rdb *redis.Client, k string) string {
	ctx := context.Background()
	var parts []string
	switch typ, _ := rdb.Type(ctx, k).Result(); typ {
	case "string":
		v, _ := rdb.Get(ctx, k).Result()
		parts = append(parts, v)
	case "list":
		parts, _ = rdb.LRange(ctx, k, 0, -1).Result()
	case "set":
		parts, _ = rdb.SMembers(ctx, k).Result()
	case "hash":
		m, _ := rdb.HGetAll(ctx, k).Result()
		for a, b := range m {
			parts = append(parts, a, b)
		}
	}
	return k + "\n" + strings.Join(parts, "\n")
}

func assertNoSentinel(t *testing.T, keys []string, dump func(string) string) {
	t.Helper()
	for _, k := range keys {
		d := dump(k)
		if strings.Contains(d, "Zzyzx") || strings.Contains(d, "41.2718") || strings.Contains(d, "96.3314") {
			t.Fatalf("key %s holds the destination", k)
		}
	}
}
