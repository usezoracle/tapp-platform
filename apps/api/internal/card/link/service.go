package link

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usezoracle/tapp/api/internal/card/token"
	"github.com/usezoracle/tapp/api/internal/ledger/movements"
)

// Service runs linking ceremonies.
type Service struct {
	Pool *pgxpool.Pool
	// MaxDailyMinor is the highest daily limit this platform will accept,
	// which is what the cardholder's verification supports. Passing it in
	// rather than reading it here keeps this package unaware of KYC tiers.
	MaxDailyMinor func(ctx context.Context, user uuid.UUID) (int64, error)
	Now           func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Start claims a card for a user and opens a session.
//
// Idempotent: asking again while a session is live returns the SAME one rather
// than opening a second. Two concurrent ceremonies on one chip would race to
// write different secrets to it, and whichever lost would leave a card its
// holder believes is linked and the server does not recognise.
func (s *Service) Start(ctx context.Context, activationToken string, user uuid.UUID) (*Session, error) {
	var session *Session

	err := movements.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		var (
			cardID uuid.UUID
			status string
			owner  *uuid.UUID
		)
		err := tx.QueryRow(ctx, `
			SELECT id, status, user_tapp_cards FROM tapp_cards
			 WHERE activation_token = $1 FOR UPDATE`, activationToken).
			Scan(&cardID, &status, &owner)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrCardUnknown
		}
		if err != nil {
			return fmt.Errorf("link: read card: %w", err)
		}

		switch {
		case status == "live":
			return ErrAlreadyLive
		case owner != nil && *owner != user:
			// Deliberately distinct from "unknown". Somebody holding a card
			// that is not theirs should be told that, not left retrying.
			return ErrCardTaken
		case status == "revoked" || status == "locked":
			return ErrAlreadyLive
		}

		// Resume a live session rather than opening a second.
		existing, err := s.liveSession(ctx, tx, cardID)
		if err != nil {
			return err
		}
		if existing != nil {
			if existing.CardID == cardID {
				session = existing
				return nil
			}
		}

		// Retire any session that has aged out before inserting the new one.
		//
		// card_link_sessions_live is a partial unique index on card_id WHERE
		// state IN ('started','provisioned') -- it has no notion of expiry,
		// while liveSession above deliberately does. So an abandoned ceremony
		// leaves a row that liveSession refuses to resume and the index
		// refuses to let anyone replace: every retry died on a 23505 the
		// handler could only report as "Something went wrong setting up your
		// card", and the card could never be linked again by anybody.
		//
		// Done here, in the same transaction as the insert, rather than left
		// to a sweeper. Service.Expire exists for this and is called from
		// nowhere; even scheduled, it would leave a window between a session
		// ageing out and the next tick in which the card is unlinkable. The
		// person retrying is the one who needs it gone, so reconcile on their
		// request and the trap cannot form.
		if _, err := tx.Exec(ctx, `
			UPDATE card_link_sessions SET state = 'abandoned', updated_at = now()
			 WHERE card_id = $1
			   AND state IN ('started', 'provisioned')
			   AND expires_at <= now()`, cardID); err != nil {
			return fmt.Errorf("link: retire expired session: %w", err)
		}

		now := s.now()
		s2 := &Session{CardID: cardID, State: StateStarted, ExpiresAt: now.Add(SessionTTL)}
		if err := tx.QueryRow(ctx, `
			INSERT INTO card_link_sessions (card_id, user_id, state, expires_at)
			VALUES ($1, $2, 'started', $3) RETURNING id`,
			cardID, user, s2.ExpiresAt).Scan(&s2.ID); err != nil {
			return fmt.Errorf("link: open session: %w", err)
		}

		if _, err := tx.Exec(ctx, `
			UPDATE tapp_cards SET status = 'claimed', user_tapp_cards = $2, updated_at = now()
			 WHERE id = $1`, cardID, user); err != nil {
			return fmt.Errorf("link: claim card: %w", err)
		}

		session = s2
		return nil
	})
	if err != nil {
		return nil, err
	}
	return session, nil
}

// Provision commits the client's proofs and issues the token to write.
//
// The token is generated here and stored, not returned and forgotten. A client
// that loses its connection between receiving it and writing it can ask again
// and get the SAME value: two different tokens written to one chip is how a
// card ends up out of sync before it has ever been used.
func (s *Service) Provision(
	ctx context.Context, sessionID, user uuid.UUID, p Provisioning,
) (*Session, error) {
	maxDaily, err := s.MaxDailyMinor(ctx, user)
	if err != nil {
		return nil, err
	}
	if err := p.Valid(maxDaily); err != nil {
		return nil, err
	}

	var session *Session
	err = movements.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		current, err := s.load(ctx, tx, sessionID, user)
		if err != nil {
			return err
		}

		switch current.State {
		case StateProvisioned:
			// Already done. Return the same token rather than issuing another.
			session = current
			return nil
		case StateStarted:
		default:
			return fmt.Errorf("%w: the session is %s", ErrWrongState, current.State)
		}

		writeToken, err := token.New()
		if err != nil {
			return err
		}

		if _, err := tx.Exec(ctx, `
			UPDATE tapp_cards
			   SET linking_proof = $2,
			       per_tap_limit_subunit = $3, step_up_threshold_subunit = $4,
			       daily_limit_subunit = $5, updated_at = now()
			 WHERE id = $1`,
			current.CardID, p.Anchor,
			p.Limits.PerTapMinor, p.Limits.StepUpMinor, p.Limits.DailyMinor); err != nil {
			return fmt.Errorf("link: provision card: %w", err)
		}

		if _, err := tx.Exec(ctx, `
			UPDATE card_link_sessions
			   SET state = 'provisioned', write_token = $2, updated_at = now()
			 WHERE id = $1`, sessionID, writeToken); err != nil {
			return fmt.Errorf("link: record provisioning: %w", err)
		}

		current.State = StateProvisioned
		current.WriteToken = hex.EncodeToString(writeToken)
		session = current
		return nil
	})
	if err != nil {
		return nil, err
	}
	return session, nil
}

// Activate completes the ceremony once the client has written the token and
// read it back.
//
// The read-back matters: an NFC write that reports success and did not land is
// common enough that trusting the write alone would leave a fraction of cards
// permanently unusable. The client proves it by presenting what it read.
func (s *Service) Activate(ctx context.Context, sessionID, user uuid.UUID, a Activation) (*Session, error) {
	if err := a.Valid(); err != nil {
		return nil, err
	}

	var session *Session

	err := movements.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		current, err := s.load(ctx, tx, sessionID, user)
		if err != nil {
			return err
		}
		if current.State == StateActivated {
			session = current
			return nil
		}
		if current.State != StateProvisioned {
			return fmt.Errorf("%w: the session is %s", ErrWrongState, current.State)
		}

		var stored []byte
		if err := tx.QueryRow(ctx,
			`SELECT write_token FROM card_link_sessions WHERE id = $1`, sessionID).
			Scan(&stored); err != nil {
			return fmt.Errorf("link: read write token: %w", err)
		}

		state := token.State{Current: stored}
		if _, err := state.Verify(a.ReadBack, s.now()); err != nil {
			// What was read back is not what was issued. The write did not
			// land, or landed corrupted; either way the card is not usable and
			// saying so now is better than at a checkout counter.
			return fmt.Errorf("%w: the card does not hold what was written to it", ErrWrongState)
		}

		// The UID is bound here rather than at provisioning, because this is
		// where the client naturally has it: the read that produced it is the
		// same read that proves the write landed.
		if _, err := tx.Exec(ctx, `
			UPDATE tapp_cards
			   SET status = 'live', card_uid_hash = $2,
			       current_token_ciphertext = $3, token_rotated_at = now(),
			       pending_token_ciphertext = NULL, pending_token_issued_at = NULL,
			       token_mismatch_count = 0, needs_resync = false,
			       pin_attempts_remaining = 5, updated_at = now()
			 WHERE id = $1`, current.CardID, a.UIDHash, stored); err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				// This chip already backs another card record. Unique since
				// this rewrite: it was merely indexed before, so two rows
				// could claim one physical card and every tap of it then
				// failed as "not recognised" -- a conflict nobody could
				// diagnose from the symptom.
				return ErrUIDTaken
			}
			return fmt.Errorf("link: activate card: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE card_link_sessions SET state = 'activated', updated_at = now() WHERE id = $1`,
			sessionID); err != nil {
			return err
		}

		current.State = StateActivated
		current.WriteToken = ""
		session = current
		return nil
	})
	if err != nil {
		return nil, err
	}
	return session, nil
}

// Get reports where a session has got to, so a client that lost its place can
// find it rather than starting over.
func (s *Service) Get(ctx context.Context, sessionID, user uuid.UUID) (*Session, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	return s.load(ctx, tx, sessionID, user)
}

func (s *Service) load(ctx context.Context, tx pgx.Tx, sessionID, user uuid.UUID) (*Session, error) {
	var (
		out        Session
		writeToken []byte
		failure    *string
	)
	err := tx.QueryRow(ctx, `
		SELECT id, card_id, state, write_token, failure, expires_at
		  FROM card_link_sessions WHERE id = $1 AND user_id = $2 FOR UPDATE`,
		sessionID, user).
		Scan(&out.ID, &out.CardID, &out.State, &writeToken, &failure, &out.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrSessionUnknown
	}
	if err != nil {
		return nil, fmt.Errorf("link: read session: %w", err)
	}

	if failure != nil {
		out.Failure = *failure
	}
	if len(writeToken) > 0 {
		out.WriteToken = hex.EncodeToString(writeToken)
	}
	if s.now().After(out.ExpiresAt) &&
		(out.State == StateStarted || out.State == StateProvisioned) {
		return nil, fmt.Errorf("%w: this linking session has expired", ErrWrongState)
	}
	return &out, nil
}

func (s *Service) liveSession(ctx context.Context, tx pgx.Tx, cardID uuid.UUID) (*Session, error) {
	var (
		out        Session
		writeToken []byte
	)
	err := tx.QueryRow(ctx, `
		SELECT id, card_id, state, write_token, expires_at
		  FROM card_link_sessions
		 WHERE card_id = $1 AND state IN ('started', 'provisioned') AND expires_at > now()`,
		cardID).Scan(&out.ID, &out.CardID, &out.State, &writeToken, &out.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("link: find live session: %w", err)
	}
	if len(writeToken) > 0 {
		out.WriteToken = hex.EncodeToString(writeToken)
	}
	return &out, nil
}

// Expire abandons sessions nobody finished, releasing their cards.
func (s *Service) Expire(ctx context.Context) (int, error) {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE card_link_sessions SET state = 'abandoned', updated_at = now()
		 WHERE state IN ('started', 'provisioned') AND expires_at < now()`)
	if err != nil {
		return 0, fmt.Errorf("link: expire sessions: %w", err)
	}
	return int(tag.RowsAffected()), nil
}
