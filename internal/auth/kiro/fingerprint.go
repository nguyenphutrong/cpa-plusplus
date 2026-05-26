package kiro

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/rand"
	"net/http"
	"sync"
	"time"
)

type Fingerprint struct {
	SDKVersion       string
	OSType           string
	OSVersion        string
	NodeVersion      string
	KiroVersion      string
	KiroHash         string
	AcceptLanguage   string
	ScreenResolution string
	ColorDepth       int
	TimezoneOffset   int
}

type FingerprintManager struct {
	mu           sync.RWMutex
	fingerprints map[string]*Fingerprint
	rng          *rand.Rand
}

func NewFingerprintManager() *FingerprintManager {
	return &FingerprintManager{
		fingerprints: make(map[string]*Fingerprint),
		rng:          rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

func (m *FingerprintManager) GetFingerprint(tokenKey string) *Fingerprint {
	m.mu.RLock()
	if fp := m.fingerprints[tokenKey]; fp != nil {
		m.mu.RUnlock()
		return fp
	}
	m.mu.RUnlock()

	m.mu.Lock()
	defer m.mu.Unlock()
	if fp := m.fingerprints[tokenKey]; fp != nil {
		return fp
	}
	fp := m.generate(tokenKey)
	m.fingerprints[tokenKey] = fp
	return fp
}

func (m *FingerprintManager) generate(tokenKey string) *Fingerprint {
	sdk := pick(m.rng, []string{"1.0.24", "1.0.25", "1.0.26", "1.0.27"})
	osType := pick(m.rng, []string{"darwin", "windows", "linux"})
	osVersion := map[string]string{"darwin": "15.1", "windows": "10.0.22631", "linux": "6.6.0"}[osType]
	nodeVersion := pick(m.rng, []string{"20.12.0", "22.1.0", "22.3.0"})
	kiroVersion := pick(m.rng, []string{"0.7.1", "0.8.0", "0.8.1"})
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%s:%s:%d", tokenKey, kiroVersion, osType, time.Now().UnixNano())))
	return &Fingerprint{
		SDKVersion:       sdk,
		OSType:           osType,
		OSVersion:        osVersion,
		NodeVersion:      nodeVersion,
		KiroVersion:      kiroVersion,
		KiroHash:         hex.EncodeToString(sum[:]),
		AcceptLanguage:   "en-US,en;q=0.9",
		ScreenResolution: "1920x1080",
		ColorDepth:       24,
		TimezoneOffset:   0,
	}
}

func pick(rng *rand.Rand, values []string) string {
	return values[rng.Intn(len(values))]
}

func (fp *Fingerprint) BuildUserAgent() string {
	return fmt.Sprintf("aws-sdk-js/%s ua/2.1 os/%s#%s lang/js md/nodejs#%s api/codewhispererstreaming#%s m/E KiroIDE-%s-%s",
		fp.SDKVersion, fp.OSType, fp.OSVersion, fp.NodeVersion, fp.SDKVersion, fp.KiroVersion, fp.KiroHash)
}

func (fp *Fingerprint) BuildAmzUserAgent() string {
	return fmt.Sprintf("aws-sdk-js/%s KiroIDE-%s-%s", fp.SDKVersion, fp.KiroVersion, fp.KiroHash)
}

func (fp *Fingerprint) ApplyToRequest(req *http.Request) {
	if req == nil {
		return
	}
	req.Header.Set("User-Agent", fp.BuildUserAgent())
	req.Header.Set("X-Amz-User-Agent", fp.BuildAmzUserAgent())
	req.Header.Set("Accept-Language", fp.AcceptLanguage)
	req.Header.Set("X-Kiro-SDK-Version", fp.SDKVersion)
	req.Header.Set("X-Kiro-OS-Type", fp.OSType)
	req.Header.Set("X-Kiro-OS-Version", fp.OSVersion)
	req.Header.Set("X-Kiro-Node-Version", fp.NodeVersion)
	req.Header.Set("X-Kiro-Version", fp.KiroVersion)
	req.Header.Set("X-Kiro-Hash", fp.KiroHash)
	req.Header.Set("X-Screen-Resolution", fp.ScreenResolution)
	req.Header.Set("X-Color-Depth", fmt.Sprintf("%d", fp.ColorDepth))
	req.Header.Set("X-Timezone-Offset", fmt.Sprintf("%d", fp.TimezoneOffset))
}
