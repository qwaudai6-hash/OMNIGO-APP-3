package validation

import (
	"fmt"
	"strings"
)

type CNICValidationResult struct {
	Valid     bool
	Formatted string
	Raw       string
	Error     string
}

func CNICValidate(input string) CNICValidationResult {
	result := CNICValidationResult{}

	cleaned := strings.ReplaceAll(strings.ReplaceAll(input, "-", ""), " ", "")
	cleaned = strings.TrimSpace(cleaned)

	if len(cleaned) == 15 && strings.Contains(input, "-") {
		parts := strings.Split(input, "-")
		if len(parts) == 3 {
			cleaned = parts[0] + parts[1] + parts[2]
		}
	}

	if !isDigitsOnly(cleaned) {
		result.Valid = false
		result.Error = "CNIC must contain only digits"
		return result
	}

	if len(cleaned) != 13 {
		result.Valid = false
		result.Error = "CNIC must be exactly 13 digits"
		return result
	}

	if !validCNICCheckDigit(cleaned) {
		result.Valid = false
		result.Error = "Invalid CNIC check digit"
		return result
	}

	result.Valid = true
	result.Raw = cleaned
	result.Formatted = CNICFormat(cleaned)
	return result
}

func CNICFormat(input string) string {
	cleaned := strings.ReplaceAll(strings.ReplaceAll(input, "-", ""), " ", "")
	cleaned = strings.TrimSpace(cleaned)

	if len(cleaned) < 13 {
		return input
	}
	if len(cleaned) > 13 {
		cleaned = cleaned[:13]
	}

	return fmt.Sprintf("%s-%s-%s",
		cleaned[0:5],
		cleaned[5:12],
		cleaned[12:13],
	)
}

func isDigitsOnly(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func validCNICCheckDigit(cnic string) bool {
	if len(cnic) != 13 {
		return false
	}

	weights := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13}

	sum := 0
	for i := 0; i < 12; i++ {
		digit := int(cnic[i] - '0')
		sum += digit * weights[i]
	}

	checkDigit := (sum % 11)
	checkDigit = 11 - checkDigit
	if checkDigit == 11 {
		checkDigit = 0
	}

	expectedCheckDigit := int(cnic[12] - '0')
	return checkDigit == expectedCheckDigit
}

func CNICFormatWithDashes(input string) string {
	cleaned := strings.ReplaceAll(strings.ReplaceAll(input, "-", ""), " ", "")
	return CNICFormat(cleaned)
}

func ExtractCNICDigits(formattedCNIC string) string {
	return strings.ReplaceAll(strings.ReplaceAll(formattedCNIC, "-", ""), " ", "")
}

type CNICInfo struct {
	AreaCode      string
	SequenceNumber string
	CheckDigit    string
	Formatted     string
}

func ParseCNIC(cnic string) (CNICInfo, error) {
	result := CNICValidate(cnic)
	if !result.Valid {
		return CNICInfo{}, fmt.Errorf("%s", result.Error)
	}

	cleaned := ExtractCNICDigits(cnic)

	return CNICInfo{
		AreaCode:       cleaned[0:5],
		SequenceNumber: cleaned[5:12],
		CheckDigit:     cleaned[12:13],
		Formatted:      result.Formatted,
	}, nil
}
