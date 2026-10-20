package onswitch

import "fmt"

// Country is the only payout country this platform sends Switch.
const Country = "NG"

// Channel is how a Nigerian payout arrives: a bank transfer.
const Channel = "BANK"

// bankCodes maps the institutions catalogue to Switch's own codes.
//
// A merchant picks their bank from the catalogue, which names banks the way
// the old aggregator did ("MONINGPC"). Switch knows banks by their NIP code
// ("090405") and nothing else. Every catalogue entry is listed, each checked
// by name against Switch's GET /institution?country=NG&channel=BANK on
// 2026-10-20; a code missing here is refused, never passed through, because a
// payout sent with a code Switch reads as another bank pays a stranger.
var bankCodes = map[string]string{
	"ABNGNGLA": "000014", // Access Bank
	"CITINGLA": "000009", // Citibank Nigeria
	"DBLNNGLA": "000005", // Diamond Bank
	"ECOCNGLA": "000010", // Ecobank Nigeria
	"FBNINGLA": "000016", // First Bank
	"FCMBNGLA": "000003", // First City Monument Bank
	"FIDTNGLA": "000007", // Fidelity Bank
	"FIRNNGLA": "000024", // Rand Merchant Bank
	"FSDHNGLA": "400001", // FSDH Merchant Bank
	"GMBLNGLA": "060004", // Greenwich Merchant Bank
	"GTBINGLA": "000013", // Guaranty Trust Bank
	"HBCLNGLA": "000020", // Heritage Bank
	"ICITNGLA": "000011", // Unity Bank
	"JAIZNGLA": "000006", // Jaiz Bank
	"KDHLNGLA": "060002", // FBNQuest Merchant Bank
	"KUDANGPC": "090267", // Kuda Microfinance Bank
	"MONINGPC": "090405", // Moniepoint Microfinance Bank
	"NAMENGLA": "000001", // Sterling Bank
	"OPAYNGPC": "100004", // OPay
	"PALMNGPC": "100033", // PalmPay
	"PAYTNGPC": "100039", // Paystack-Titan MFB
	"PLNINGLA": "000002", // Keystone Bank
	"PRDTNGLA": "000008", // Polaris Bank
	"PROVNGLA": "000023", // Providus Bank
	"SAHVNGPC": "090286", // Safe Haven Microfinance Bank
	"SBICNGLA": "000012", // Stanbic IBTC Bank
	"SCBLNGLA": "000021", // Standard Chartered Bank
	"SUTGNGLA": "000022", // Suntrust Bank
	"UBNINGLA": "000018", // Union Bank
	"UNAFNGLA": "000004", // United Bank for Africa
	"WEMANGLA": "000017", // Wema Bank
	"ZEIBNGLA": "000015", // Zenith Bank
}

// UnmappedBankError says a stored institution code has no Switch code.
// Retrying cannot help until the code is added here.
type UnmappedBankError struct{ Code string }

func (e *UnmappedBankError) Error() string {
	return fmt.Sprintf("switch: no Switch bank code for institution %q", e.Code)
}

// BankCode translates a catalogue code into Switch's.
func BankCode(catalogue string) (string, error) {
	if code, ok := bankCodes[catalogue]; ok {
		return code, nil
	}
	return "", &UnmappedBankError{Code: catalogue}
}
