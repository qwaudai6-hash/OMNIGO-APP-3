package handlers

import (
	"os"
	"testing"
)

func TestPostMessageTargetOrigin(t *testing.T) {
	t.Run("PAYFAST_WEB_ORIGIN_TakesPriority", func(t *testing.T) {
		t.Setenv("PAYFAST_WEB_ORIGIN", "https://myapp.com")
		t.Setenv("CORS_ALLOWED_ORIGINS", "https://other.com")

		origin, err := postMessageTargetOrigin()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if origin != "https://myapp.com" {
			t.Errorf("Expected PAYFAST_WEB_ORIGIN, got %q", origin)
		}
		t.Logf("✅ PAYFAST_WEB_ORIGIN takes priority: %s", origin)
	})

	t.Run("FallsBackToCORSTrustedOrigin", func(t *testing.T) {
		os.Unsetenv("PAYFAST_WEB_ORIGIN")
		t.Setenv("CORS_ALLOWED_ORIGINS", "https://trusted.com,https://other.com")

		origin, err := postMessageTargetOrigin()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if origin != "https://trusted.com" {
			t.Errorf("Expected first CORS origin, got %q", origin)
		}
		t.Logf("✅ Falls back to first CORS origin: %s", origin)
	})

	t.Run("RejectsWildcardCORS", func(t *testing.T) {
		os.Unsetenv("PAYFAST_WEB_ORIGIN")
		t.Setenv("CORS_ALLOWED_ORIGINS", "*")

		_, err := postMessageTargetOrigin()
		if err == nil {
			t.Error("Should return error when only wildcard CORS is set")
		}
		t.Logf("✅ Wildcard CORS correctly rejected")
	})

	t.Run("ErrorWhenNothingSet", func(t *testing.T) {
		os.Unsetenv("PAYFAST_WEB_ORIGIN")
		os.Unsetenv("CORS_ALLOWED_ORIGINS")

		_, err := postMessageTargetOrigin()
		if err == nil {
			t.Error("Should return error when no origin is configured")
		}
		t.Logf("✅ Returns error when no origin configured")
	})

	t.Run("EmptyStringInCORSPrevented", func(t *testing.T) {
		os.Unsetenv("PAYFAST_WEB_ORIGIN")
		t.Setenv("CORS_ALLOWED_ORIGINS", "  ,  ")

		_, err := postMessageTargetOrigin()
		if err == nil {
			t.Error("Should return error when CORS has only whitespace entries")
		}
		t.Logf(" whitespace-only CORS entries correctly rejected")
	})
}
