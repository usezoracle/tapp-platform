package naira

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/usezoracle/tapp/api/services/baas"
)

// fintavaBanks is the rail's list as it really reads: upper case, with the
// generic words the catalogue's names spell differently.
var fintavaBanks = []baas.Bank{
	{Name: "MONIEPOINT MICROFINANCE BANK", BankCode: "090405"},
	{Name: "OPAY", BankCode: "090325"},
	{Name: "PALMPAY", BankCode: "090338"},
	{Name: "KUDA MICROFINANCE BANK", BankCode: "090267"},
	{Name: "GTBANK PLC", BankCode: "058"},
	{Name: "ACCESS BANK", BankCode: "044"},
	{Name: "ACCESS (DIAMOND) BANK", BankCode: "063"},
	{Name: "ZENITH BANK", BankCode: "057"},
	{Name: "UNITED BANK FOR AFRICA", BankCode: "033"},
	{Name: "FIRST BANK OF NIGERIA", BankCode: "011"},
	{Name: "FIRST CITY MONUMENT BANK", BankCode: "214"},
	{Name: "FIDELITY BANK", BankCode: "070"},
	{Name: "STERLING BANK", BankCode: "232"},
	{Name: "WEMA BANK", BankCode: "035"},
	{Name: "POLARIS BANK", BankCode: "076"},
	{Name: "UNION BANK OF NIGERIA", BankCode: "032"},
	{Name: "STANBIC IBTC BANK", BankCode: "221"},
	{Name: "PROVIDUS BANK", BankCode: "101"},
	{Name: "VFD MICROFINANCE BANK", BankCode: "090110"},
	{Name: "CARBON", BankCode: "565"},
	{Name: "KUBA MICROFINANCE BANK", BankCode: "090999"},
	{Name: "LOMA MICROFINANCE BANK", BankCode: "090620"},
}

// paycrestInstitutions is the catalogue as seeded: the aggregator's codes.
var paycrestInstitutions = []Institution{
	{Code: "MONINGPC", Name: "Moniepoint Microfinance Bank"},
	{Code: "OPAYNGPC", Name: "OPay"},
	{Code: "PALMNGPC", Name: "PalmPay"},
	{Code: "KUDANGPC", Name: "Kuda Microfinance Bank"},
	{Code: "GTBINGLA", Name: "Guaranty Trust Bank"},
	{Code: "ABNGNGLA", Name: "Access Bank"},
	{Code: "ZEIBNGLA", Name: "Zenith Bank"},
	{Code: "UNAFNGLA", Name: "United Bank for Africa"},
	{Code: "FBNINGLA", Name: "First Bank of Nigeria"},
	{Code: "FCMBNGLA", Name: "First City Monument Bank"},
	{Code: "FIDTNGLA", Name: "Fidelity Bank"},
	{Code: "NAMENGLA", Name: "Sterling Bank"},
	{Code: "WEMANGLA", Name: "Wema Bank"},
	{Code: "PRDTNGLA", Name: "Polaris Bank"},
	{Code: "UBNINGLA", Name: "Union Bank of Nigeria"},
	{Code: "SBICNGLA", Name: "Stanbic IBTC Bank"},
	{Code: "UMPLNGLA", Name: "Providus Bank"},
	{Code: "VFDMNGLA", Name: "VFD Microfinance Bank"},
	{Code: "CRBNNGLA", Name: "Carbon"},
	{Code: "SFHVNGLA", Name: "Safe Haven Microfinance Bank"},
}

func stubResolver(banks []baas.Bank, institutions []Institution) *BankCodeResolver {
	return &BankCodeResolver{
		Banks:        func(context.Context) ([]baas.Bank, error) { return banks, nil },
		Institutions: func(context.Context) ([]Institution, error) { return institutions, nil },
	}
}

// The production failure: the merchant saved Moniepoint under the
// catalogue's code, and the rail wants its own.
func TestACatalogueCodeIsTranslatedToTheRails(t *testing.T) {
	r := stubResolver(fintavaBanks, paycrestInstitutions)
	ctx := context.Background()

	code, name, err := r.FintavaCode(ctx, "MONINGPC")
	if err != nil || code != "090405" || name != "MONIEPOINT MICROFINANCE BANK" {
		t.Fatalf("MONINGPC -> %q %q %v, want 090405 Moniepoint", code, name, err)
	}

	// Every common bank, by whichever spelling the two lists disagree on.
	for stored, want := range map[string]string{
		"OPAYNGPC": "090325", "PALMNGPC": "090338", "KUDANGPC": "090267",
		"GTBINGLA": "058", "ABNGNGLA": "044", "ZEIBNGLA": "057", "UNAFNGLA": "033",
		"FBNINGLA": "011", "FCMBNGLA": "214", "FIDTNGLA": "070", "NAMENGLA": "232",
		"WEMANGLA": "035", "PRDTNGLA": "076", "UBNINGLA": "032", "SBICNGLA": "221",
		"UMPLNGLA": "101", "VFDMNGLA": "090110", "CRBNNGLA": "565",
	} {
		code, _, err := r.FintavaCode(ctx, stored)
		if err != nil || code != want {
			t.Errorf("%s -> %q %v, want %s", stored, code, err, want)
		}
	}
}

// A code the rail already lists is used as it is: numeric codes stored
// before the catalogue, or the rail's own.
func TestARailCodePassesThrough(t *testing.T) {
	r := stubResolver(fintavaBanks, paycrestInstitutions)
	code, name, err := r.FintavaCode(context.Background(), "090405")
	if err != nil || code != "090405" || name != "MONIEPOINT MICROFINANCE BANK" {
		t.Fatalf("090405 -> %q %q %v", code, name, err)
	}
}

// A bank the rail does not list is a typed error that names the
// institution, and never a guess.
func TestAnUnmappedBankIsATypedError(t *testing.T) {
	r := stubResolver(fintavaBanks, paycrestInstitutions)
	ctx := context.Background()

	_, _, err := r.FintavaCode(ctx, "SFHVNGLA")
	var unmapped *UnmappedBankError
	if !errors.As(err, &unmapped) {
		t.Fatalf("SFHVNGLA -> %v, want *UnmappedBankError", err)
	}
	if unmapped.Error() != "no Fintava sort code for Safe Haven Microfinance Bank (SFHVNGLA)" {
		t.Errorf("message = %q", unmapped.Error())
	}

	// Not in the catalogue either.
	_, _, err = r.FintavaCode(ctx, "NOPE")
	if !errors.As(err, &unmapped) || unmapped.StoredCode != "NOPE" || unmapped.Institution != "" {
		t.Fatalf("NOPE -> %v", err)
	}

	// Word-level containment: "UBA" is not found inside "KUBA", and a
	// bank whose distinctive name is a generic word matches nothing.
	only := stubResolver([]baas.Bank{{Name: "KUBA MICROFINANCE BANK", BankCode: "090999"}},
		[]Institution{{Code: "UNAFNGLA", Name: "UBA"}, {Code: "BANK", Name: "Bank"}})
	for _, stored := range []string{"UNAFNGLA", "BANK"} {
		if _, _, err := only.FintavaCode(ctx, stored); !errors.As(err, &unmapped) {
			t.Errorf("%s -> %v, want unmapped", stored, err)
		}
	}
}

// Both lists are fetched once an hour, not once a payout; a list that
// cannot be refreshed is served stale.
func TestListsAreCachedForAnHour(t *testing.T) {
	banksCalls, instCalls := 0, 0
	var bankErr error
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	r := &BankCodeResolver{
		Banks: func(context.Context) ([]baas.Bank, error) {
			banksCalls++
			return fintavaBanks, bankErr
		},
		Institutions: func(context.Context) ([]Institution, error) {
			instCalls++
			return paycrestInstitutions, nil
		},
		Now: func() time.Time { return now },
	}
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if _, _, err := r.FintavaCode(ctx, "MONINGPC"); err != nil {
			t.Fatal(err)
		}
	}
	if banksCalls != 1 || instCalls != 1 {
		t.Fatalf("fetched banks %d times, institutions %d times; want once each", banksCalls, instCalls)
	}

	now = now.Add(time.Hour + time.Second)
	bankErr = errors.New("fintava: 503")
	code, _, err := r.FintavaCode(ctx, "MONINGPC")
	if err != nil || code != "090405" {
		t.Fatalf("after a failed refresh: %q %v, want the stale list", code, err)
	}
	if banksCalls != 2 || instCalls != 2 {
		t.Fatalf("after an hour: banks %d, institutions %d; want a refresh of each", banksCalls, instCalls)
	}

	// A resolver that has never had a list cannot answer.
	cold := &BankCodeResolver{Banks: func(context.Context) ([]baas.Bank, error) { return nil, bankErr }}
	if _, _, err := cold.FintavaCode(ctx, "090405"); !errors.Is(err, bankErr) {
		t.Fatalf("cold: %v, want the fetch error", err)
	}
}
