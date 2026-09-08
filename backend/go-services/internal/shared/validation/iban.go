package validation

import (
	"fmt"
	"math/big"
	"regexp"
	"strings"
)

var (
	ibanRegex       = regexp.MustCompile(`(?i)^pk[0-9]{2}[a-z0-9]{4}[0-9]{16}$`)
	ibanSpacesRegex = regexp.MustCompile(`(?i)^pk[0-9]{2}[ ][a-z0-9]{4}[ ][0-9]{4}[ ][0-9]{4}[ ][0-9]{4}[ ][0-9]{4}[ ][0-9]{4}$`)
	ibanBasicRegex  = regexp.MustCompile(`(?i)^pk[0-9]{2}[a-z0-9]{4}[0-9]{16}$`)
	cnicRegex       = regexp.MustCompile(`^[0-9]{13}$`)
	cnicDashRegex   = regexp.MustCompile(`^[0-9]{5}-[0-9]{7}-[0-9]$`)
)

var pakistanBankCodes = map[string]string{
	"HABB": "Habib Bank Limited",
	"UBLL": "United Bank Limited",
	"MCBL": "MCB Bank Limited",
	"BAFL": "Bank Alfalah",
	"ASKB": "Askari Bank",
	"SCBL": "Standard Chartered Bank",
	"MEZB": "Meezan Bank",
	"BIMN": "BankIslami Pakistan",
	"SILK": "Silkbank",
	"ALFB": "Al Baraka Bank",
	"BOFA": "Bank of Punjab",
	"HABP": "Habib Bank Limited (Old)",
	"FINL": "Finca Microfinance Bank",
	"ZARL": "Zarai Taraqiati Bank",
	"NRBL": "National Bank of Pakistan",
	"BLKL": "Black Rock Microfinance",
	"UPBL": "The Bank of Khyber",
	"FAIS": "Faysal Bank",
	"MFBL": "Mobilink Microfinance Bank",
	"OGDC": "Oil & Gas Development Company",
}

type IBANValidationResult struct {
	Valid         bool
	Formatted    string
	Raw          string
	BankCode     string
	BankName     string
	BranchCode   string
	AccountNumber string
	Error        string
}

func IBANValidate(input string) IBANValidationResult {
	result := IBANValidationResult{Raw: input}

	cleaned := strings.ReplaceAll(strings.ReplaceAll(input, " ", ""), "-", "")
	cleaned = strings.ToUpper(cleaned)

	if len(cleaned) < 22 || len(cleaned) > 30 {
		result.Valid = false
		result.Error = "IBAN must be 22-24 characters (PK prefix + 20-22 digits)"
		return result
	}

	if !strings.HasPrefix(cleaned, "PK") {
		result.Valid = false
		result.Error = "IBAN must start with PK (Pakistan country code)"
		return result
	}

	checkDigits := cleaned[2:4]
	if len(checkDigits) != 2 || checkDigits < "02" || checkDigits > "97" {
		result.Valid = false
		result.Error = "Invalid check digits (must be 02-97)"
		return result
	}

	bankCode := cleaned[4:8]
	if !regexp.MustCompile(`^[A-Z0-9]{4}$`).MatchString(bankCode) {
		result.Valid = false
		result.Error = "Invalid bank code format"
		return result
	}

	formatted := fmt.Sprintf("PK%s %s %s %s %s %s",
		cleaned[2:4],
		cleaned[4:8],
		cleaned[8:12],
		cleaned[12:16],
		cleaned[16:20],
		cleaned[20:24],
	)

	result.Formatted = formatted
	result.BankCode = bankCode
	result.BankName = pakistanBankCodes[bankCode]
	if result.BankName == "" {
		result.BankName = "Unknown Bank"
	}

	if !mod97Check(cleaned) {
		result.Valid = false
		result.Error = "IBAN checksum validation failed"
		return result
	}

	accountNumberRegex := regexp.MustCompile(`^[A-Z0-9]{12,16}$`)
	accountPart := cleaned[8:]
	if !accountNumberRegex.MatchString(accountPart) {
		result.Valid = false
		result.Error = "Invalid account number format"
		return result
	}

	result.Valid = true
	result.AccountNumber = accountPart
	return result
}

func mod97Check(iban string) bool {
	rearranged := iban[4:] + iban[:4]

	var numericIBAN strings.Builder
	for _, char := range rearranged {
		if char >= 'A' && char <= 'Z' {
			numericIBAN.WriteString(fmt.Sprintf("%d", int(char-'A'+10)))
		} else {
			numericIBAN.WriteString(string(char))
		}
	}

	ibanBigInt := new(big.Int)
	ibanBigInt.SetString(numericIBAN.String(), 10)

	remainder := new(big.Int)
	remainder.Mod(ibanBigInt, big.NewInt(97))

	return remainder.Int64() == 1
}

func IBANFormat(input string) string {
	cleaned := strings.ReplaceAll(strings.ReplaceAll(input, " ", ""), "-", "")
	cleaned = strings.ToUpper(cleaned)

	if len(cleaned) < 22 {
		return input
	}

	if len(cleaned) > 24 {
		cleaned = cleaned[:24]
	}

	formatted := fmt.Sprintf("PK%s %s %s %s %s %s",
		cleaned[2:4],
		cleaned[4:8],
		cleaned[8:12],
		cleaned[12:16],
		cleaned[16:20],
		cleaned[20:24],
	)

	return formatted
}

func GetBankNameFromCode(code string) string {
	if name, ok := pakistanBankCodes[strings.ToUpper(code)]; ok {
		return name
	}
	return "Unknown Bank"
}

func GetAllPakistanBanks() map[string]string {
	banks := make(map[string]string)
	for k, v := range pakistanBankCodes {
		banks[k] = v
	}
	return banks
}
