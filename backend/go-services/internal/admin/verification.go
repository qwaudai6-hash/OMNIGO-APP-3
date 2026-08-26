package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// VerificationDetail is the enriched KYC/KYB review payload.
type VerificationDetail struct {
	TrackingID         string `json:"tracking_id"`
	Email              string `json:"email"`
	FullName           string `json:"full_name"`
	Role               string `json:"role"`
	Phone              string `json:"phone"`
	BusinessName       string `json:"business_name"`
	Address            string `json:"address"`
	CNICURL            string `json:"cnic_url"`
	LicenseURL         string `json:"license_url"`
	VerificationStatus string `json:"verification_status"`
	RiskScore          int    `json:"risk_score"`
	OCRText            string `json:"ocr_text"`
	SubmittedAt        string `json:"submitted_at,omitempty"`
	VerifiedAt         string `json:"verified_at,omitempty"`
	Reason             string `json:"verification_reason,omitempty"`
}

// VerificationService orchestrates KYC/KYB review, OCR, and risk scoring.
type VerificationService struct {
	db *pgxpool.Pool
}

func NewVerificationService(db *pgxpool.Pool) *VerificationService {
	return &VerificationService{db: db}
}

const (
	StatusUnverified = "unverified"
	StatusPending    = "pending"
	StatusApproved   = "approved"
	StatusRejected   = "rejected"
)

// SubmitVerification moves a user to pending and runs OCR + risk scoring.
func (s *VerificationService) SubmitVerification(ctx context.Context, trackingID string) (*VerificationDetail, error) {
	user, err := s.getUserForVerification(ctx, trackingID)
	if err != nil {
		return nil, err
	}

	ocrText := s.runOCR(user.CNICURL) + " " + s.runOCR(user.LicenseURL)
	score := s.calculateRiskScore(ctx, user, ocrText)

	status := StatusPending
	reason := ""
	if score >= 60 {
		status = StatusRejected
		reason = "High risk score triggered automatic rejection"
	} else if score <= 20 {
		status = StatusApproved
		reason = "Low risk score triggered automatic approval"
	}

	now := time.Now()
	query := `
		UPDATE users
		SET verification_status = $1,
		    risk_score = $2,
		    submitted_at = $3,
		    verification_reason = $4,
		    is_verified = $5,
		    updated_at = NOW()
		WHERE tracking_id = $6
	`
	isVerified := status == StatusApproved
	_, err = s.db.Exec(ctx, query, status, score, now, reason, isVerified, trackingID)
	if err != nil {
		return nil, fmt.Errorf("failed to submit verification: %w", err)
	}

	detail := user
	detail.VerificationStatus = status
	detail.RiskScore = score
	detail.OCRText = ocrText
	detail.SubmittedAt = now.Format(time.RFC3339)
	detail.Reason = reason
	if isVerified {
		detail.VerifiedAt = now.Format(time.RFC3339)
	}
	return &detail, nil
}

// GetVerification returns a single verification detail.
func (s *VerificationService) GetVerification(ctx context.Context, trackingID string) (*VerificationDetail, error) {
	user, err := s.getUserForVerification(ctx, trackingID)
	if err != nil {
		return nil, err
	}
	return &user, nil
}

// ListPendingVerifications returns users awaiting manual review.
func (s *VerificationService) ListPendingVerifications(ctx context.Context, limit, offset int) ([]VerificationDetail, int, error) {
	countQuery := `SELECT COUNT(*) FROM users WHERE verification_status = 'pending' AND role IN ('rider', 'vendor')`
	var total int
	if err := s.db.QueryRow(ctx, countQuery).Scan(&total); err != nil {
		return nil, 0, err
	}

	query := `
		SELECT tracking_id, email, COALESCE(full_name, ''), role, COALESCE(phone, ''), COALESCE(business_name, ''), COALESCE(address, ''), COALESCE(cnic_url, ''), COALESCE(license_url, ''), verification_status, risk_score, COALESCE(verification_reason, ''), COALESCE(submitted_at::TEXT, ''), COALESCE(verified_at::TEXT, '')
		FROM users
		WHERE verification_status = 'pending' AND role IN ('rider', 'vendor')
		ORDER BY submitted_at ASC
		LIMIT $1 OFFSET $2
	`
	rows, err := s.db.Query(ctx, query, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var users []VerificationDetail
	for rows.Next() {
		var u VerificationDetail
		if err := rows.Scan(&u.TrackingID, &u.Email, &u.FullName, &u.Role, &u.Phone, &u.BusinessName, &u.Address, &u.CNICURL, &u.LicenseURL, &u.VerificationStatus, &u.RiskScore, &u.Reason, &u.SubmittedAt, &u.VerifiedAt); err != nil {
			return nil, 0, err
		}
		users = append(users, u)
	}
	return users, total, rows.Err()
}

// ApproveVerification manually approves a user and cascades activation to their store and products.
func (s *VerificationService) ApproveVerification(ctx context.Context, trackingID, reason string) error {
	query := `
		UPDATE users
		SET is_verified = true,
		    verification_status = 'approved',
		    verification_reason = $1,
		    verified_at = NOW(),
		    updated_at = NOW()
		WHERE tracking_id = $2
	`
	_, err := s.db.Exec(ctx, query, reason, trackingID)
	if err != nil {
		return err
	}

	// Cascade activation to vendor stores and products so their catalog is
	// immediately visible. Best-effort after the user is already verified,
	// but failures are logged loudly for ops follow-up instead of swallowed.
	if _, err := s.db.Exec(ctx, `UPDATE stores SET is_active = true, updated_at = NOW() WHERE vendor_tracking_id = $1`, trackingID); err != nil {
		log.Printf("[ADMIN] CRITICAL: store activation cascade failed for vendor %s: %v", trackingID, err)
	}
	if _, err := s.db.Exec(ctx, `UPDATE products SET is_active = true, updated_at = NOW() WHERE vendor_tracking_id = $1`, trackingID); err != nil {
		log.Printf("[ADMIN] CRITICAL: product activation cascade failed for vendor %s: %v", trackingID, err)
	}

	return nil
}

// RejectVerification manually rejects a user.
func (s *VerificationService) RejectVerification(ctx context.Context, trackingID, reason string) error {
	if strings.TrimSpace(reason) == "" {
		return fmt.Errorf("rejection reason is required")
	}
	query := `
		UPDATE users
		SET is_verified = false,
		    verification_status = 'rejected',
		    verification_reason = $1,
		    verified_at = NOW(),
		    updated_at = NOW()
		WHERE tracking_id = $2
	`
	_, err := s.db.Exec(ctx, query, reason, trackingID)
	return err
}

// getUserForVerification loads the user row enriched for review.
func (s *VerificationService) getUserForVerification(ctx context.Context, trackingID string) (VerificationDetail, error) {
	query := `
		SELECT tracking_id, email, COALESCE(full_name, ''), role, COALESCE(phone, ''), COALESCE(business_name, ''), COALESCE(address, ''), COALESCE(cnic_url, ''), COALESCE(license_url, ''), verification_status, risk_score, COALESCE(verification_reason, ''), COALESCE(submitted_at::TEXT, ''), COALESCE(verified_at::TEXT, '')
		FROM users
		WHERE tracking_id = $1
	`
	var u VerificationDetail
	err := s.db.QueryRow(ctx, query, trackingID).Scan(
		&u.TrackingID, &u.Email, &u.FullName, &u.Role, &u.Phone, &u.BusinessName, &u.Address, &u.CNICURL, &u.LicenseURL, &u.VerificationStatus, &u.RiskScore, &u.Reason, &u.SubmittedAt, &u.VerifiedAt,
	)
	if err != nil {
		return VerificationDetail{}, fmt.Errorf("user not found: %w", err)
	}
	return u, nil
}

// runOCR extracts text from an uploaded document via external OCR service.
// Uses configurable OCR endpoint (e.g. Tesseract microservice, Google Vision API).
// Falls back to basic file metadata if OCR service is unavailable.
func (s *VerificationService) runOCR(docURL string) string {
	if docURL == "" {
		return ""
	}

	ocrURL := os.Getenv("OCR_SERVICE_URL")
	if ocrURL == "" {
		// No OCR service configured — read raw bytes as basic content signal
		path := strings.TrimPrefix(docURL, "/uploads/kyc/")
		fullPath := filepath.Join("./uploads/kyc", filepath.Base(path))
		data, err := os.ReadFile(fullPath)
		if err != nil {
			return ""
		}
		return fmt.Sprintf("file:%s bytes:%d", filepath.Base(fullPath), len(data))
	}

	// Call external OCR service (e.g. POST multipart to Tesseract API)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	path := strings.TrimPrefix(docURL, "/uploads/kyc/")
	fullPath := filepath.Join("./uploads/kyc", filepath.Base(path))

	file, err := os.Open(fullPath)
	if err != nil {
		return ""
	}
	defer file.Close()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", filepath.Base(fullPath))
	if err != nil {
		return ""
	}
	if _, err := io.Copy(part, file); err != nil {
		return ""
	}
	writer.Close()

	req, err := http.NewRequestWithContext(ctx, "POST", ocrURL, body)
	if err != nil {
		return ""
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	var ocrResp struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&ocrResp); err != nil {
		return ""
	}
	return ocrResp.Text
}

// calculateRiskScore applies rule-based scoring with sanctions screening.
func (s *VerificationService) calculateRiskScore(ctx context.Context, user VerificationDetail, ocrText string) int {
	score := 0
	if user.CNICURL == "" || user.LicenseURL == "" {
		score += 50
	}
	if strings.TrimSpace(ocrText) == "" {
		score += 25
	}
	if user.Phone == "" {
		score += 20
	}
	if user.Address == "" {
		score += 10
	}
	// Sanctions screening against OFAC SDN list (configurable external service)
	sanctionsScore := s.checkSanctions(ctx, user.FullName, user.BusinessName)
	score += sanctionsScore
	return score
}

// checkSanctions queries an external sanctions screening service (e.g. ComplyAdvantage, Dow Jones).
// Returns 100 if the name appears on a sanctions list, 0 otherwise.
func (s *VerificationService) checkSanctions(ctx context.Context, fullName, businessName string) int {
	sanctionsURL := os.Getenv("SANCTIONS_SCREENING_URL")
	if sanctionsURL == "" {
		// No sanctions service configured — skip screening
		return 0
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	payload := map[string]string{
		"full_name":     fullName,
		"business_name": businessName,
	}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, "POST", sanctionsURL, bytes.NewReader(body))
	if err != nil {
		return 0
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey := os.Getenv("SANCTIONS_API_KEY"); apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Printf("[Sanctions] Screening service unreachable: %v\n", err)
		return 0
	}
	defer resp.Body.Close()

	var result struct {
		IsSanctioned bool   `json:"is_sanctioned"`
		RiskLevel    string `json:"risk_level"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0
	}

	if result.IsSanctioned {
		fmt.Printf("[Sanctions] MATCH FOUND: %s / %s (risk=%s)\n", fullName, businessName, result.RiskLevel)
		return 100
	}
	return 0
}
