package v1

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/usezoracle/tapp/api/internal/ledger"
)

// Who took the money. A cardholder's activity has to say WHERE a card was
// spent, and the ledger only knows the merchant by its sender profile id. The
// name is resolved here, once per page, rather than joined into the ledger's
// own history query: the ledger is the record of money and should not learn
// about businesses and users to describe itself.

// merchantView names a merchant on the wire.
type merchantView struct {
	// Ref is the sender profile id: the same id Freedom knows the merchant by.
	Ref  string `json:"ref"`
	Name string `json:"name"`
	// Symbol is the business's listed ticker, or null when it has none.
	Symbol *string `json:"symbol"`
}

// merchantNameSQL is the naming rule, in one place: the business's trading
// name when the sender has registered one (its legal name if the trading name
// is blank), otherwise the person behind the profile. The symbol is only shown
// once the listing is live; a submitted or rejected ticker is not a security.
//
// It expects a row alias `s` holding the sender id and joins from there.
const merchantNameSQL = `
	LEFT JOIN merchant_businesses mb ON mb.sender_id = s.sender_id
	LEFT JOIN sender_profiles sp ON sp.id = s.sender_id
	LEFT JOIN users u ON u.id = sp.user_sender_profile`

const merchantNameCols = `
	COALESCE(NULLIF(mb.trading_name, ''), NULLIF(mb.legal_name, ''),
	         NULLIF(TRIM(COALESCE(u.first_name, '') || ' ' || COALESCE(u.last_name, '')), ''), ''),
	CASE WHEN mb.state = 'listed' THEN NULLIF(mb.symbol, '') END`

// merchantsOfTaps names the merchant of each tap, keyed by tap id, in one
// query. A tap that is not in card_taps (it should not happen; the row is
// written in the tap's own transaction) is simply absent from the map.
func merchantsOfTaps(ctx context.Context, q ledger.Querier, tapIDs []uuid.UUID) (map[uuid.UUID]merchantView, error) {
	out := map[uuid.UUID]merchantView{}
	if len(tapIDs) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `
		SELECT s.tap_id, s.sender_id, `+merchantNameCols+`
		  FROM (SELECT id AS tap_id, merchant_id AS sender_id FROM card_taps WHERE id = ANY($1)) s`+
		merchantNameSQL, tapIDs)
	if err != nil {
		return nil, fmt.Errorf("merchants of taps: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var tap, sender uuid.UUID
		var m merchantView
		if err := rows.Scan(&tap, &sender, &m.Name, &m.Symbol); err != nil {
			return nil, fmt.Errorf("merchants of taps: %w", err)
		}
		m.Ref = sender.String()
		out[tap] = m
	}
	return out, rows.Err()
}

// merchantsBySender names merchants by sender profile id, in one query. A
// sender nobody has heard of is absent from the map.
func merchantsBySender(ctx context.Context, q ledger.Querier, senderIDs []uuid.UUID) (map[uuid.UUID]merchantView, error) {
	out := map[uuid.UUID]merchantView{}
	if len(senderIDs) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `
		SELECT s.sender_id, `+merchantNameCols+`
		  FROM unnest($1::uuid[]) AS s(sender_id)`+
		merchantNameSQL+`
		 WHERE mb.sender_id IS NOT NULL OR sp.id IS NOT NULL`, senderIDs)
	if err != nil {
		return nil, fmt.Errorf("merchants by sender: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var sender uuid.UUID
		var m merchantView
		if err := rows.Scan(&sender, &m.Name, &m.Symbol); err != nil {
			return nil, fmt.Errorf("merchants by sender: %w", err)
		}
		m.Ref = sender.String()
		out[sender] = m
	}
	return out, rows.Err()
}
