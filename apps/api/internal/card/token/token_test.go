package token

import (
	"bytes"
	"errors"
	"testing"
	"time"
)

func fresh(t *testing.T) []byte {
	t.Helper()
	b, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if len(b) != Len {
		t.Fatalf("token is %d bytes, want %d", len(b), Len)
	}
	return b
}

func TestTokensAreUnpredictable(t *testing.T) {
	a, b := fresh(t), fresh(t)
	if bytes.Equal(a, b) {
		t.Fatal("two tokens came back identical")
	}
}

func TestTheOrdinaryCaseIsTheCurrentToken(t *testing.T) {
	cur := fresh(t)
	s := State{Current: cur}

	got, err := s.Verify(cur, time.Now())
	if err != nil || got != MatchesCurrent {
		t.Fatalf("Verify(current) = %v, %v; want MatchesCurrent", got, err)
	}
}

// The whole reason two-phase rotation exists. A debit issues a token, the NFC
// write lands, but the acknowledgement never reaches the server. The card now
// presents the pending token, and it must work -- the predecessor rotated
// immediately, so this case bricked a legitimate card and sent its holder into
// a resync flow that iOS cannot run.
func TestACardPresentingAnUnacknowledgedTokenStillWorks(t *testing.T) {
	cur, pending := fresh(t), fresh(t)
	issued := time.Now()
	s := State{Current: cur, Pending: pending, PendingIssuedAt: &issued}

	got, err := s.Verify(pending, issued.Add(time.Minute))
	if err != nil || got != MatchesPending {
		t.Fatalf("Verify(pending) = %v, %v; want MatchesPending", got, err)
	}
}

// And the other half: if the write failed, the card still has the old token
// and that must keep working too. Both are live until one is acknowledged.
func TestACardWhoseWriteFailedStillWorks(t *testing.T) {
	cur, pending := fresh(t), fresh(t)
	issued := time.Now()
	s := State{Current: cur, Pending: pending, PendingIssuedAt: &issued}

	got, err := s.Verify(cur, issued.Add(time.Minute))
	if err != nil || got != MatchesCurrent {
		t.Fatalf("Verify(current, with a pending outstanding) = %v, %v; want MatchesCurrent", got, err)
	}
}

// An unacknowledged token does not expire. The card holding it is the card
// that was written; a lost acknowledgement an hour ago is still a lost
// acknowledgement, and the card must keep working until its next tap
// promotes the token. See Verify.
func TestAnUnacknowledgedTokenDoesNotExpire(t *testing.T) {
	cur, pending := fresh(t), fresh(t)
	issued := time.Now()
	s := State{Current: cur, Pending: pending, PendingIssuedAt: &issued}

	later := issued.Add(24 * time.Hour)
	if got, err := s.Verify(pending, later); err != nil || got != MatchesPending {
		t.Fatalf("a day-old pending token was refused: %v, %v", got, err)
	}
	if got, err := s.Verify(cur, later); err != nil || got != MatchesCurrent {
		t.Fatalf("the current token stopped working alongside an old pending one: %v, %v", got, err)
	}
}

// A clone presenting a token from some earlier tap is refused. This is the
// detection the whole scheme exists for.
func TestAStaleTokenIsRefused(t *testing.T) {
	stale, cur := fresh(t), fresh(t)
	s := State{Current: cur}

	if _, err := s.Verify(stale, time.Now()); !errors.Is(err, ErrMismatch) {
		t.Fatalf("a stale token was accepted: %v", err)
	}
}

func TestMalformedAndMissingTokens(t *testing.T) {
	cur := fresh(t)

	if _, err := (State{}).Verify(cur, time.Now()); !errors.Is(err, ErrNotProvisioned) {
		t.Error("a card with no token did not report as unprovisioned")
	}
	s := State{Current: cur}
	for name, presented := range map[string][]byte{
		"empty":     {},
		"too short": cur[:Len-1],
		"too long":  append(append([]byte{}, cur...), 0x00),
	} {
		if _, err := s.Verify(presented, time.Now()); !errors.Is(err, ErrMismatch) {
			t.Errorf("%s token was not refused", name)
		}
	}
}
