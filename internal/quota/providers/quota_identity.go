package providers

import "strings"

func normalizeAccountIdentity(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func isGenericProviderAccountIdentity(providerID, identity string) bool {
	normalizedProvider := strings.ToLower(strings.TrimSpace(providerID))
	normalizedIdentity := normalizeAccountIdentity(identity)
	if normalizedProvider == "" || normalizedIdentity == "" {
		return false
	}
	return strings.EqualFold(normalizedIdentity, normalizedProvider+" account")
}

func isValidAccountIdentityForProvider(providerID, identity string) bool {
	normalizedProvider := strings.ToLower(strings.TrimSpace(providerID))
	normalizedIdentity := normalizeAccountIdentity(identity)
	if normalizedIdentity == "" {
		return false
	}
	if normalizedIdentity == normalizedProvider {
		return false
	}
	if isGenericProviderAccountIdentity(normalizedProvider, normalizedIdentity) {
		return false
	}
	if strings.Contains(normalizedIdentity, "@") {
		return true
	}
	if strings.EqualFold(normalizedProvider, "kiro") && strings.HasPrefix(normalizedIdentity, "d-") {
		return true
	}
	if strings.EqualFold(normalizedProvider, "github-copilot") {
		return true
	}
	return false
}

func derivedAccountIdentity(input QuotaFetchInput) string {
	normalizedProvider := strings.ToLower(strings.TrimSpace(input.ProviderID))
	normalizedID := strings.ToLower(strings.TrimSpace(input.CredentialID))
	if normalizedProvider == "github-copilot" && strings.HasPrefix(normalizedID, "copilot-") {
		login := strings.TrimPrefix(normalizedID, "copilot-")
		if isValidAccountIdentityForProvider(normalizedProvider, login) {
			return normalizeAccountIdentity(login)
		}
	}
	return ""
}
