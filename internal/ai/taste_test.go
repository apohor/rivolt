package ai

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// fakeTaste records what the operator provider asks of its metering.
type fakeTaste struct {
	mu      sync.Mutex
	takeErr error
	takes   int
	refunds int
	spent   []float64
}

func (f *fakeTaste) Take(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.takes++
	return f.takeErr
}
func (f *fakeTaste) Refund(context.Context) { f.mu.Lock(); f.refunds++; f.mu.Unlock() }
func (f *fakeTaste) Spent(_ context.Context, usd float64) {
	f.mu.Lock()
	f.spent = append(f.spent, usd)
	f.mu.Unlock()
}

const opusUsage = `{"input_tokens":1000,"output_tokens":500}`

func okReply() fakeReply {
	return ok("claude-opus-5-5", "end_turn", `[{"type":"text","text":"hi"}]`, opusUsage)
}

func TestOperatorKeyNeedsTheTasteMark(t *testing.T) {
	f := &fakeAnthropic{replies: []fakeReply{okReply()}}
	p := newTest(t, f, OperatorModel, true, nil)
	// A reservation alone is not enough: a route that forgot the taste fails closed.
	if _, _, err := p.Complete(WithReservation(context.Background()), "s", "u"); !errors.Is(err, ErrOperatorDenied) {
		t.Fatalf("err = %v, want ErrOperatorDenied", err)
	}
	// And a taste alone is not enough either.
	if _, _, err := p.Complete(WithTaste(context.Background(), NoTaste), "s", "u"); !errors.Is(err, ErrOperatorDenied) {
		t.Fatalf("err = %v, want ErrOperatorDenied", err)
	}
	if f.calls() != 0 {
		t.Fatalf("the provider was called %d times without both marks", f.calls())
	}
}

func TestRefusedTakeNeverReachesTheProvider(t *testing.T) {
	for _, e := range []error{ErrTasteUsed, ErrTasteCeiling, errors.New("store down")} {
		f := &fakeAnthropic{replies: []fakeReply{okReply()}}
		p := newTest(t, f, OperatorModel, true, nil)
		ft := &fakeTaste{takeErr: e}
		_, _, err := p.Complete(WithTaste(WithReservation(context.Background()), ft), "s", "u")
		if !errors.Is(err, e) {
			t.Errorf("err = %v, want %v", err, e)
		}
		if f.calls() != 0 || ft.refunds != 0 {
			t.Errorf("%v: calls=%d refunds=%d, want 0 and 0", e, f.calls(), ft.refunds)
		}
	}
}

func TestAnswerSpendsAndSettles(t *testing.T) {
	f := &fakeAnthropic{replies: []fakeReply{okReply()}}
	p := newTest(t, f, OperatorModel, true, nil)
	ft := &fakeTaste{}
	if _, _, err := p.Complete(WithTaste(WithReservation(context.Background()), ft), "s", "u"); err != nil {
		t.Fatal(err)
	}
	if ft.takes != 1 || ft.refunds != 0 || len(ft.spent) != 1 || ft.spent[0] <= 0 {
		t.Fatalf("takes=%d refunds=%d spent=%v, want one take, no refund, one positive settlement", ft.takes, ft.refunds, ft.spent)
	}
}

func TestProviderFailureRefundsButAnAnswerDoesNot(t *testing.T) {
	cases := map[string]struct {
		reply   fakeReply
		refunds int
	}{
		"client error": {fakeReply{400, `{"type":"error","error":{"type":"invalid_request_error","message":"x"}}`}, 1},
		"refusal":      {ok("claude-opus-5-5", "refusal", `[]`, opusUsage), 0},
		"cut off":      {ok("claude-opus-5-5", "max_tokens", `[{"type":"text","text":"{"}]`, opusUsage), 0},
	}
	for name, c := range cases {
		f := &fakeAnthropic{replies: []fakeReply{c.reply}}
		p := newTest(t, f, OperatorModel, true, nil)
		ft := &fakeTaste{}
		if _, _, err := p.Complete(WithTaste(WithReservation(context.Background()), ft), "s", "u"); err == nil {
			t.Errorf("%s: want an error", name)
		}
		if ft.refunds != c.refunds {
			t.Errorf("%s: refunds = %d, want %d", name, ft.refunds, c.refunds)
		}
	}
}

func TestOwnKeyIsNotMetered(t *testing.T) {
	f := &fakeAnthropic{replies: []fakeReply{okReply()}}
	p := newTest(t, f, "claude-sonnet-5-5", false, nil)
	ft := &fakeTaste{}
	if _, _, err := p.Complete(WithTaste(context.Background(), ft), "s", "u"); err != nil {
		t.Fatal(err)
	}
	if ft.takes != 0 || len(ft.spent) != 0 {
		t.Fatalf("a user's own key touched the taste: takes=%d spent=%v", ft.takes, ft.spent)
	}
}
