package management

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	_ "modernc.org/sqlite"
)

const antigravityIDEProvider = "antigravity"

var antigravityIDEDBPathOverride string

type antigravityActiveAccountRequest struct {
	ID string `json:"id"`
}

type antigravityActiveAccountResponse struct {
	Provider        string `json:"provider"`
	ActiveID        string `json:"active_id"`
	AccountIdentity string `json:"account_identity"`
	ProjectID       string `json:"project_id,omitempty"`
	IDEPatched      bool   `json:"ide_patched"`
	RestartRequired bool   `json:"restart_required"`
}

type antigravityIDEToken struct {
	AccessToken     string
	RefreshToken    string
	IDToken         string
	ExpiryTimestamp int64
	Email           string
	Name            string
	ProjectID       string
	IsGCPTOS        bool
	OAuthClientKey  string
}

func (h *Handler) PostAntigravityActiveAccount(c *gin.Context) {
	if h == nil || h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}

	var req antigravityActiveAccountRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	id := strings.TrimSpace(req.ID)
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "id is required"})
		return
	}

	auth, ok := h.authManager.GetByID(id)
	if !ok || auth == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "provider credential not found"})
		return
	}
	if strings.ToLower(strings.TrimSpace(auth.Provider)) != antigravityIDEProvider {
		c.JSON(http.StatusBadRequest, gin.H{"error": "provider credential is not antigravity"})
		return
	}
	if _, errRefresh := h.refreshAntigravityOAuthAccessToken(c.Request.Context(), auth); errRefresh != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to refresh antigravity access token"})
		return
	}

	token, errToken := antigravityIDETokenFromAuth(auth)
	if errToken != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errToken.Error()})
		return
	}

	if errPatch := patchAntigravityIDEActiveAccount(token); errPatch != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to patch Antigravity IDE account: %v", errPatch)})
		return
	}

	now := time.Now().UTC()
	for _, item := range h.authManager.List() {
		if item == nil || strings.ToLower(strings.TrimSpace(item.Provider)) != antigravityIDEProvider {
			continue
		}
		if item.Metadata == nil {
			item.Metadata = map[string]any{}
		}
		if item.Attributes == nil {
			item.Attributes = map[string]string{}
		}
		if item.ID == auth.ID {
			item.Metadata["active_account"] = true
			item.Metadata["priority"] = 1
			item.Attributes["priority"] = "1"
		} else {
			delete(item.Metadata, "active_account")
			delete(item.Metadata, "priority")
			delete(item.Attributes, "priority")
		}
		item.UpdatedAt = now
		if _, errUpdate := h.authManager.Update(c.Request.Context(), item); errUpdate != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to update provider credential: %v", errUpdate)})
			return
		}
	}

	c.JSON(http.StatusOK, antigravityActiveAccountResponse{
		Provider:        antigravityIDEProvider,
		ActiveID:        auth.ID,
		AccountIdentity: token.Email,
		ProjectID:       token.ProjectID,
		IDEPatched:      true,
		RestartRequired: true,
	})
}

func antigravityIDETokenFromAuth(auth *coreauth.Auth) (antigravityIDEToken, error) {
	if auth == nil {
		return antigravityIDEToken{}, errors.New("provider credential not found")
	}
	metadata := auth.Metadata
	token := antigravityIDEToken{
		AccessToken:    strings.TrimSpace(tokenValueFromMetadata(metadata)),
		RefreshToken:   strings.TrimSpace(stringValue(metadata, "refresh_token")),
		IDToken:        strings.TrimSpace(stringValue(metadata, "id_token")),
		Email:          strings.TrimSpace(providerAccountIdentity(auth)),
		Name:           strings.TrimSpace(firstProviderNonEmpty(auth.Label, stringValue(metadata, "name"), authAttribute(auth, "name"))),
		ProjectID:      strings.TrimSpace(firstProviderNonEmpty(authProjectID(auth), stringValue(metadata, "project_id"), stringValue(metadata, "enterprise_project_id"))),
		OAuthClientKey: strings.TrimSpace(stringValue(metadata, "oauth_client_key")),
	}
	if token.Email == "" {
		token.Email = strings.TrimSpace(firstProviderNonEmpty(stringValue(metadata, "email"), authAttribute(auth, "email")))
	}
	if token.Name == "" {
		token.Name = token.Email
	}
	token.IsGCPTOS = boolValue(metadata["is_gcp_tos"])
	if token.OAuthClientKey == "antigravity_enterprise" {
		token.IsGCPTOS = false
	}
	token.ExpiryTimestamp = antigravityExpiryTimestamp(metadata)

	switch {
	case token.AccessToken == "":
		return antigravityIDEToken{}, errors.New("antigravity access token missing")
	case token.RefreshToken == "":
		return antigravityIDEToken{}, errors.New("antigravity refresh token missing")
	case token.Email == "":
		return antigravityIDEToken{}, errors.New("antigravity account identity missing")
	case token.ExpiryTimestamp <= 0:
		return antigravityIDEToken{}, errors.New("antigravity token expiry missing")
	}
	return token, nil
}

func antigravityExpiryTimestamp(metadata map[string]any) int64 {
	if metadata == nil {
		return 0
	}
	for _, key := range []string{"expiry_timestamp", "expiryTimestamp", "expires_at_unix", "expiresAtUnix"} {
		if value := int64Value(metadata[key]); value > 0 {
			if value > 1_000_000_000_000 {
				return value / 1000
			}
			return value
		}
	}
	if expStr := strings.TrimSpace(stringValue(metadata, "expired")); expStr != "" {
		if ts, errParse := time.Parse(time.RFC3339, expStr); errParse == nil {
			return ts.Unix()
		}
	}
	if expStr := strings.TrimSpace(stringValue(metadata, "expiry")); expStr != "" {
		if ts, errParse := time.Parse(time.RFC3339, expStr); errParse == nil {
			return ts.Unix()
		}
	}
	expiresIn := int64Value(metadata["expires_in"])
	timestampMs := int64Value(metadata["timestamp"])
	if expiresIn > 0 && timestampMs > 0 {
		return time.UnixMilli(timestampMs).Add(time.Duration(expiresIn) * time.Second).Unix()
	}
	return 0
}

func boolValue(raw any) bool {
	switch typed := raw.(type) {
	case bool:
		return typed
	case string:
		v := strings.ToLower(strings.TrimSpace(typed))
		return v == "true" || v == "1" || v == "yes"
	case int, int32, int64, uint, uint32, uint64, float32, float64, json.Number:
		return int64Value(typed) != 0
	default:
		return false
	}
}

func patchAntigravityIDEActiveAccount(token antigravityIDEToken) error {
	dbPath, checked := antigravityIDEStateDBPath()
	if dbPath == "" {
		return fmt.Errorf("Antigravity IDE database not found. Checked paths: %s", strings.Join(checked, ", "))
	}
	if errBackup := backupAntigravityIDEDB(dbPath); errBackup != nil {
		return errBackup
	}

	db, errOpen := sql.Open("sqlite", dbPath)
	if errOpen != nil {
		return errOpen
	}
	defer func() { _ = db.Close() }()
	if _, errExec := db.Exec("PRAGMA busy_timeout = 3000"); errExec != nil {
		return errExec
	}
	if errValidate := validateAntigravityIDEStateDB(db); errValidate != nil {
		return errValidate
	}
	return injectAntigravityIDEToken(db, token)
}

func validateAntigravityIDEStateDB(db *sql.DB) error {
	var name string
	err := db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='ItemTable'").Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("Antigravity IDE ItemTable not found")
	}
	if err != nil {
		return err
	}
	return nil
}

func injectAntigravityIDEToken(db *sql.DB, token antigravityIDEToken) error {
	capability, errCapability := detectAntigravityIDEFormatCapability(db)
	if errCapability != nil {
		return errCapability
	}
	if capability == "" || capability == "dual" {
		newErr := injectAntigravityIDENewFormat(db, token)
		oldErr := injectAntigravityIDEOldFormat(db, token)
		if newErr != nil && oldErr != nil {
			return fmt.Errorf("token injection failed for both Antigravity IDE formats")
		}
		return nil
	}
	if capability == "new" {
		return injectAntigravityIDENewFormat(db, token)
	}
	return injectAntigravityIDEOldFormat(db, token)
}

func detectAntigravityIDEFormatCapability(db *sql.DB) (string, error) {
	unified, errUnified := itemValueExists(db, "antigravityUnifiedStateSync.oauthToken")
	if errUnified != nil {
		return "", errUnified
	}
	old, errOld := itemValueExists(db, "jetskiStateSync.agentManagerInitState")
	if errOld != nil {
		return "", errOld
	}
	if unified && old {
		return "dual", nil
	}
	if unified {
		return "new", nil
	}
	if old {
		return "old", nil
	}
	return "dual", nil
}

func itemValueExists(db *sql.DB, key string) (bool, error) {
	var value sql.NullString
	err := db.QueryRow("SELECT value FROM ItemTable WHERE key = ?", key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return value.Valid && value.String != "", nil
}

func upsertAntigravityIDEItem(tx *sql.Tx, key, value string) error {
	_, err := tx.Exec("INSERT INTO ItemTable(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value", key, value)
	return err
}

func deleteAntigravityIDEItem(tx *sql.Tx, key string) error {
	_, err := tx.Exec("DELETE FROM ItemTable WHERE key = ?", key)
	return err
}

func writeAntigravityIDEAuthStatus(tx *sql.Tx, token antigravityIDEToken) error {
	raw, errMarshal := json.Marshal(map[string]string{
		"name":   firstProviderNonEmpty(token.Name, token.Email),
		"email":  token.Email,
		"apiKey": token.AccessToken,
	})
	if errMarshal != nil {
		return errMarshal
	}
	if errUpsert := upsertAntigravityIDEItem(tx, "antigravityAuthStatus", string(raw)); errUpsert != nil {
		return errUpsert
	}
	if errUpsert := upsertAntigravityIDEItem(tx, "antigravityOnboarding", "true"); errUpsert != nil {
		return errUpsert
	}
	return deleteAntigravityIDEItem(tx, "google.antigravity")
}

func injectAntigravityIDENewFormat(db *sql.DB, token antigravityIDEToken) error {
	tx, errBegin := db.Begin()
	if errBegin != nil {
		return errBegin
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	oauthToken := createUnifiedOAuthToken(token.AccessToken, token.RefreshToken, token.ExpiryTimestamp, token.IsGCPTOS, token.IDToken, token.Email)
	if errUpsert := upsertAntigravityIDEItem(tx, "antigravityUnifiedStateSync.oauthToken", oauthToken); errUpsert != nil {
		return errUpsert
	}
	userStatusEntry := createUnifiedStateEntry("userStatusSentinelKey", createMinimalUserStatusPayload(token.Email))
	if errUpsert := upsertAntigravityIDEItem(tx, "antigravityUnifiedStateSync.userStatus", userStatusEntry); errUpsert != nil {
		return errUpsert
	}
	if strings.TrimSpace(token.ProjectID) != "" {
		projectEntry := createUnifiedStateEntry("enterpriseGcpProjectId", createStringValuePayload(token.ProjectID))
		if errUpsert := upsertAntigravityIDEItem(tx, "antigravityUnifiedStateSync.enterprisePreferences", projectEntry); errUpsert != nil {
			return errUpsert
		}
	} else if errDelete := deleteAntigravityIDEItem(tx, "antigravityUnifiedStateSync.enterprisePreferences"); errDelete != nil {
		return errDelete
	}
	if errStatus := writeAntigravityIDEAuthStatus(tx, token); errStatus != nil {
		return errStatus
	}
	if errCommit := tx.Commit(); errCommit != nil {
		return errCommit
	}
	committed = true
	return nil
}

func injectAntigravityIDEOldFormat(db *sql.DB, token antigravityIDEToken) error {
	tx, errBegin := db.Begin()
	if errBegin != nil {
		return errBegin
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	var encodedState sql.NullString
	errQuery := tx.QueryRow("SELECT value FROM ItemTable WHERE key = ?", "jetskiStateSync.agentManagerInitState").Scan(&encodedState)
	if errQuery != nil && !errors.Is(errQuery, sql.ErrNoRows) {
		return errQuery
	}
	if encodedState.Valid && strings.TrimSpace(encodedState.String) != "" {
		stateBytes, errDecode := base64.StdEncoding.DecodeString(encodedState.String)
		if errDecode != nil {
			return errDecode
		}
		withoutToken, errRemove := removeProtobufField(stateBytes, 6)
		if errRemove != nil {
			return errRemove
		}
		updated := append(withoutToken, createOAuthTokenInfo(token.AccessToken, token.RefreshToken, token.ExpiryTimestamp)...)
		if errUpsert := upsertAntigravityIDEItem(tx, "jetskiStateSync.agentManagerInitState", base64.StdEncoding.EncodeToString(updated)); errUpsert != nil {
			return errUpsert
		}
	}
	if errStatus := writeAntigravityIDEAuthStatus(tx, token); errStatus != nil {
		return errStatus
	}
	if errCommit := tx.Commit(); errCommit != nil {
		return errCommit
	}
	committed = true
	return nil
}

func backupAntigravityIDEDB(dbPath string) error {
	backupPath := fmt.Sprintf("%s.cpa-backup-%s", dbPath, time.Now().UTC().Format("20060102T150405.000000000Z"))
	in, errOpen := os.Open(dbPath)
	if errOpen != nil {
		return fmt.Errorf("failed to open Antigravity IDE database for backup: %w", errOpen)
	}
	defer func() { _ = in.Close() }()
	out, errCreate := os.OpenFile(backupPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errCreate != nil {
		return fmt.Errorf("failed to create Antigravity IDE database backup: %w", errCreate)
	}
	defer func() { _ = out.Close() }()
	if _, errCopy := out.ReadFrom(in); errCopy != nil {
		return fmt.Errorf("failed to copy Antigravity IDE database backup: %w", errCopy)
	}
	return nil
}

func antigravityIDEStateDBPath() (string, []string) {
	candidates := antigravityIDEStateDBPathCandidates()
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if info, errStat := os.Stat(candidate); errStat == nil && !info.IsDir() {
			return candidate, candidates
		}
	}
	return "", candidates
}

func antigravityIDEStateDBPathCandidates() []string {
	var paths []string
	appendUnique := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		for _, existing := range paths {
			if existing == value {
				return
			}
		}
		paths = append(paths, value)
	}
	if antigravityIDEDBPathOverride != "" {
		appendUnique(antigravityIDEDBPathOverride)
	}
	if envPath := os.Getenv("CPA_ANTIGRAVITY_IDE_DB_PATH"); envPath != "" {
		appendUnique(envPath)
	}
	home, _ := os.UserHomeDir()
	appData := antigravityIDEAppDataDir(home)
	pushUserData := func(userData string) {
		appendUnique(filepath.Join(userData, "User", "globalStorage", "state.vscdb"))
		appendUnique(filepath.Join(userData, "User", "state.vscdb"))
		appendUnique(filepath.Join(userData, "state.vscdb"))
	}
	if appData != "" {
		pushUserData(appData)
	}
	if runtime.GOOS == "darwin" && home != "" {
		appendUnique(filepath.Join(home, "Library", "Application Support", "Antigravity IDE", "state.vscdb"))
	}
	return paths
}

func antigravityIDEAppDataDir(home string) string {
	switch runtime.GOOS {
	case "darwin":
		if home == "" {
			return ""
		}
		return filepath.Join(home, "Library", "Application Support", "Antigravity IDE")
	case "windows":
		appData := os.Getenv("APPDATA")
		if appData == "" && home != "" {
			appData = filepath.Join(home, "AppData", "Roaming")
		}
		if appData == "" {
			return ""
		}
		return filepath.Join(appData, "Antigravity IDE")
	case "linux":
		if antigravityRunningInWSL() {
			if windowsUser := antigravityWindowsUserFromWSL(home); windowsUser != "" {
				return filepath.Join("/mnt/c/Users", windowsUser, "AppData", "Roaming", "Antigravity IDE")
			}
		}
		if home == "" {
			return ""
		}
		return filepath.Join(home, ".config", "Antigravity IDE")
	default:
		if home == "" {
			return ""
		}
		return filepath.Join(home, ".antigravity-ide")
	}
}

func antigravityRunningInWSL() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	data, errRead := os.ReadFile("/proc/version")
	if errRead != nil {
		return false
	}
	version := strings.ToLower(string(data))
	return strings.Contains(version, "microsoft") && strings.Contains(version, "wsl")
}

func antigravityWindowsUserFromWSL(home string) string {
	if user := strings.TrimSpace(os.Getenv("USERNAME")); user != "" {
		if info, errStat := os.Stat(filepath.Join("/mnt/c/Users", user)); errStat == nil && info.IsDir() {
			return user
		}
	}
	if base := filepath.Base(home); base != "." && base != string(filepath.Separator) && base != "" {
		if info, errStat := os.Stat(filepath.Join("/mnt/c/Users", base)); errStat == nil && info.IsDir() {
			return base
		}
	}
	entries, errRead := os.ReadDir("/mnt/c/Users")
	if errRead != nil {
		return ""
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		switch strings.ToLower(name) {
		case "public", "default", "default user", "all users":
			continue
		}
		return name
	}
	return ""
}

func encodeVarint(value uint64) []byte {
	out := make([]byte, 0, 10)
	for value >= 128 {
		out = append(out, byte(value&127|128))
		value >>= 7
	}
	return append(out, byte(value))
}

func readVarint(data []byte, offset int) (uint64, int, error) {
	var result uint64
	var shift uint
	for pos := offset; pos < len(data); pos++ {
		b := data[pos]
		if shift >= 64 {
			return 0, 0, errors.New("protobuf varint overflow")
		}
		result |= uint64(b&127) << shift
		if b&128 == 0 {
			return result, pos + 1, nil
		}
		shift += 7
	}
	return 0, 0, errors.New("incomplete protobuf varint")
}

func skipProtobufField(data []byte, offset int, wireType int) (int, error) {
	switch wireType {
	case 0:
		_, next, err := readVarint(data, offset)
		return next, err
	case 1:
		return offset + 8, nil
	case 2:
		length, next, err := readVarint(data, offset)
		if err != nil {
			return 0, err
		}
		end := next + int(length)
		if end < next || end > len(data) {
			return 0, errors.New("protobuf length exceeds buffer")
		}
		return end, nil
	case 5:
		return offset + 4, nil
	default:
		return 0, fmt.Errorf("unknown protobuf wire type: %d", wireType)
	}
}

func removeProtobufField(data []byte, fieldNum int) ([]byte, error) {
	result := make([]byte, 0, len(data))
	offset := 0
	for offset < len(data) {
		start := offset
		tag, next, err := readVarint(data, offset)
		if err != nil {
			return nil, err
		}
		wireType := int(tag & 7)
		currentField := int(tag >> 3)
		end, errSkip := skipProtobufField(data, next, wireType)
		if errSkip != nil {
			return nil, errSkip
		}
		if end > len(data) {
			return nil, errors.New("protobuf field exceeds buffer")
		}
		if currentField != fieldNum {
			result = append(result, data[start:end]...)
		}
		offset = end
	}
	return result, nil
}

func encodeLenDelimField(fieldNum int, data []byte) []byte {
	out := encodeVarint(uint64(fieldNum<<3 | 2))
	out = append(out, encodeVarint(uint64(len(data)))...)
	out = append(out, data...)
	return out
}

func encodeStringField(fieldNum int, value string) []byte {
	return encodeLenDelimField(fieldNum, []byte(value))
}

func encodeVarintField(fieldNum int, value int64) []byte {
	out := encodeVarint(uint64(fieldNum << 3))
	out = append(out, encodeVarint(uint64(value))...)
	return out
}

func createTimestampField(fieldNum int, seconds int64) []byte {
	return encodeLenDelimField(fieldNum, encodeVarintField(1, seconds))
}

func createOAuthTokenInfo(accessToken, refreshToken string, expiry int64) []byte {
	payload := make([]byte, 0)
	payload = append(payload, encodeStringField(1, accessToken)...)
	payload = append(payload, encodeStringField(2, "Bearer")...)
	payload = append(payload, encodeStringField(3, refreshToken)...)
	payload = append(payload, createTimestampField(4, expiry)...)
	return encodeLenDelimField(6, payload)
}

func createOAuthInfo(accessToken, refreshToken string, expiry int64, isGCPTOS bool, idToken, email string) []byte {
	if email != "" && isPersonalAntigravityAccountEmail(email) && isGCPTOS {
		isGCPTOS = false
	}
	out := make([]byte, 0)
	out = append(out, encodeStringField(1, accessToken)...)
	out = append(out, encodeStringField(2, "Bearer")...)
	out = append(out, encodeStringField(3, refreshToken)...)
	timestamp := append(encodeVarintField(1, expiry), encodeVarintField(2, 0)...)
	out = append(out, encodeLenDelimField(4, timestamp)...)
	if idToken != "" {
		out = append(out, encodeStringField(5, idToken)...)
	}
	if isGCPTOS {
		out = append(out, encodeVarintField(6, 1)...)
	}
	return out
}

func createUnifiedOAuthToken(accessToken, refreshToken string, expiry int64, isGCPTOS bool, idToken, email string) string {
	return createUnifiedStateEntry("oauthTokenInfoSentinelKey", createOAuthInfo(accessToken, refreshToken, expiry, isGCPTOS, idToken, email))
}

func createUnifiedStateEntry(sentinelKey string, payload []byte) string {
	row := encodeStringField(1, base64.StdEncoding.EncodeToString(payload))
	dataEntry := append(encodeStringField(1, sentinelKey), encodeLenDelimField(2, row)...)
	topic := encodeLenDelimField(1, dataEntry)
	return base64.StdEncoding.EncodeToString(topic)
}

func createStringValuePayload(value string) []byte {
	return encodeStringField(3, value)
}

func createMinimalUserStatusPayload(email string) []byte {
	return append(encodeStringField(3, email), encodeStringField(7, email)...)
}

func isPersonalAntigravityAccountEmail(email string) bool {
	lower := strings.ToLower(email)
	return strings.HasSuffix(lower, "@gmail.com") || strings.HasSuffix(lower, "@outlook.com") || strings.HasSuffix(lower, "@hotmail.com") || strings.HasSuffix(lower, "@qq.com") || strings.HasSuffix(lower, "@163.com")
}
