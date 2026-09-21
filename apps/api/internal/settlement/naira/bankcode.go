package naira

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/usezoracle/tapp/api/services/baas"
)

// Two vocabularies name the same banks.
//
// A merchant picks their bank from the institutions catalogue, which is
// Paycrest's: "MONINGPC" is Moniepoint. The rail that pays the naira leg is
// Fintava, which knows Moniepoint as "090405" and nothing as "MONINGPC". The
// first real payout sent the catalogue's code to the rail, the rail could
// not resolve the account, and the merchant was not paid.
//
// BankCodeResolver translates. It is the only place a code is chosen for the
// rail, and it never lets an untranslated one through: a code it cannot map
// is an *UnmappedBankError, which the worker records on the leg as failed
// with the institution's name in the message.

// UnmappedBankError says the stored code names a bank the rail has no code
// for. Retrying cannot help until the rail lists the bank or the merchant
// saves a different one.
type UnmappedBankError struct {
	// StoredCode is what the merchant's account carries: a catalogue code.
	StoredCode string
	// Institution is the catalogue's name for it, or empty if the
	// catalogue does not know the code either.
	Institution string
}

func (e *UnmappedBankError) Error() string {
	name := e.Institution
	if name == "" {
		name = "an institution the catalogue does not list"
	}
	return fmt.Sprintf("no Fintava sort code for %s (%s)", name, e.StoredCode)
}

// Institution is one catalogue entry: the code a merchant's account stores,
// and the bank's name.
type Institution struct {
	Code string
	Name string
}

// BankCodeResolver maps a stored institution code to the rail's own code.
//
// Both lists are read through functions so the process wires the real ones
// (the rail's ListBanks, the catalogue table) and a test hands in fixtures.
// Each is cached for TTL; a list that cannot be refreshed is served stale
// rather than failing every payout for the length of a rail outage.
type BankCodeResolver struct {
	// Banks lists the rail's beneficiary banks: name and sort code.
	Banks func(ctx context.Context) ([]baas.Bank, error)
	// Institutions lists the catalogue the stored codes come from. Nil
	// means there is no catalogue: only codes the rail already knows
	// resolve.
	Institutions func(ctx context.Context) ([]Institution, error)
	// TTL is how long each list is trusted. Zero means an hour.
	TTL time.Duration
	Now func() time.Time

	mu           sync.Mutex
	banks        []baas.Bank
	banksAt      time.Time
	institutions []Institution
	instAt       time.Time
}

// DefaultBankListTTL is how long the lists are cached when TTL is unset.
const DefaultBankListTTL = time.Hour

// FintavaCode returns the rail's code for storedCode, and the rail's name
// for the bank.
//
//   - A code the rail already lists passes through: numeric codes saved
//     before the catalogue existed, or a rail's own code stored directly.
//   - Otherwise the catalogue gives the institution's name, and that is
//     matched against the rail's names.
//   - Nothing else is guessed. An *UnmappedBankError names what could not
//     be mapped.
func (r *BankCodeResolver) FintavaCode(ctx context.Context, storedCode string) (code, bankName string, err error) {
	storedCode = strings.TrimSpace(storedCode)
	if storedCode == "" {
		return "", "", &UnmappedBankError{}
	}
	banks, err := r.bankList(ctx)
	if err != nil {
		return "", "", fmt.Errorf("naira: list rail banks: %w", err)
	}
	for _, b := range banks {
		if b.BankCode == storedCode {
			return b.BankCode, b.Name, nil
		}
	}

	institutions, err := r.institutionList(ctx)
	if err != nil {
		return "", "", fmt.Errorf("naira: list institutions: %w", err)
	}
	var institution string
	for _, i := range institutions {
		if i.Code == storedCode {
			institution = i.Name
			break
		}
	}
	if institution == "" {
		return "", "", &UnmappedBankError{StoredCode: storedCode}
	}
	if b, ok := matchBankName(institution, banks); ok {
		return b.BankCode, b.Name, nil
	}
	return "", "", &UnmappedBankError{StoredCode: storedCode, Institution: institution}
}

func (r *BankCodeResolver) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *BankCodeResolver) ttl() time.Duration {
	if r.TTL > 0 {
		return r.TTL
	}
	return DefaultBankListTTL
}

func (r *BankCodeResolver) bankList(ctx context.Context) ([]baas.Bank, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.banks != nil && r.now().Sub(r.banksAt) < r.ttl() {
		return r.banks, nil
	}
	if r.Banks == nil {
		return nil, errors.New("no bank list configured")
	}
	banks, err := r.Banks(ctx)
	if err != nil {
		if r.banks != nil {
			return r.banks, nil
		}
		return nil, err
	}
	if banks == nil {
		banks = []baas.Bank{}
	}
	r.banks, r.banksAt = banks, r.now()
	return banks, nil
}

func (r *BankCodeResolver) institutionList(ctx context.Context) ([]Institution, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.institutions != nil && r.now().Sub(r.instAt) < r.ttl() {
		return r.institutions, nil
	}
	if r.Institutions == nil {
		return []Institution{}, nil
	}
	list, err := r.Institutions(ctx)
	if err != nil {
		if r.institutions != nil {
			return r.institutions, nil
		}
		return nil, err
	}
	if list == nil {
		list = []Institution{}
	}
	r.institutions, r.instAt = list, r.now()
	return list, nil
}

// Name matching, the way the backbone does it: lowercase, drop the words
// every bank has (bank, mfb, microfinance, limited, plc, nigeria), then
// either name containing the other is a match. Containment is by whole
// words, not characters -- "uba" must not be found inside "Kuba MFB".
//
// Banks whose two names share no words are in the alias table: the
// catalogue says "Guaranty Trust Bank" where the rail says "GTBank".

// bankAliases groups the spellings one bank goes by. An institution name
// carrying any spelling in a group matches a rail name carrying any
// spelling in the same group. Spellings are matched on the lowercased name
// with punctuation removed but the generic words kept, so "first bank" is
// found in "First Bank of Nigeria" and not in "First City Monument Bank".
var bankAliases = [][]string{
	{"moniepoint"},
	{"opay", "paycom"},
	{"palmpay"},
	{"kuda"},
	{"gtbank", "gt bank", "guaranty trust", "gtb"},
	{"access bank", "access"},
	{"zenith"},
	{"uba", "united bank for africa"},
	{"first bank", "firstbank", "fbn"},
	{"fidelity"},
	{"fcmb", "first city monument"},
	{"sterling"},
	{"wema"},
	{"polaris"},
	{"union bank"},
	{"stanbic", "stanbic ibtc"},
	{"providus"},
	{"vfd"},
	{"carbon", "one finance"},
}

// genericWords carry no identity: every bank has them.
var genericWords = map[string]bool{
	"bank": true, "banks": true, "mfb": true, "microfinance": true, "micro": true, "finance": true,
	"limited": true, "ltd": true, "plc": true, "nigeria": true, "nig": true, "the": true, "of": true,
}

// words lowercases a name and splits it into words, punctuation removed.
func words(name string) []string {
	var out []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for _, c := range strings.ToLower(name) {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			cur.WriteRune(c)
		default:
			flush()
		}
	}
	flush()
	return out
}

// distinctive is words less the generic ones.
func distinctive(name string) []string {
	var out []string
	for _, w := range words(name) {
		if !genericWords[w] {
			out = append(out, w)
		}
	}
	return out
}

// containsWords reports whether needle appears in hay as a run of whole
// words.
func containsWords(hay, needle []string) bool {
	if len(needle) == 0 || len(needle) > len(hay) {
		return false
	}
outer:
	for i := 0; i+len(needle) <= len(hay); i++ {
		for j := range needle {
			if hay[i+j] != needle[j] {
				continue outer
			}
		}
		return true
	}
	return false
}

// aliasGroup finds the alias group a name belongs to, if any.
func aliasGroup(name string) int {
	ws := words(name)
	for g, group := range bankAliases {
		for _, spelling := range group {
			if containsWords(ws, words(spelling)) {
				return g
			}
		}
	}
	return -1
}

// matchBankName finds the rail's entry for a catalogue name. Exact
// normalised equality wins; then the alias table; then whole-word
// containment either way, preferring the rail name with the fewest words
// left over.
func matchBankName(institution string, banks []baas.Bank) (baas.Bank, bool) {
	want := distinctive(institution)
	if len(want) == 0 {
		return baas.Bank{}, false
	}
	wantKey := strings.Join(want, " ")
	for _, b := range banks {
		if strings.Join(distinctive(b.Name), " ") == wantKey {
			return b, true
		}
	}

	if g := aliasGroup(institution); g >= 0 {
		var best baas.Bank
		bestLen := -1
		for _, b := range banks {
			if aliasGroup(b.Name) != g {
				continue
			}
			if n := len(distinctive(b.Name)); bestLen < 0 || n < bestLen {
				best, bestLen = b, n
			}
		}
		if bestLen >= 0 {
			return best, true
		}
	}

	var best baas.Bank
	bestExtra := -1
	for _, b := range banks {
		have := distinctive(b.Name)
		if len(have) == 0 {
			continue
		}
		if !containsWords(have, want) && !containsWords(want, have) {
			continue
		}
		extra := len(have) - len(want)
		if extra < 0 {
			extra = -extra
		}
		if bestExtra < 0 || extra < bestExtra {
			best, bestExtra = b, extra
		}
	}
	return best, bestExtra >= 0
}
