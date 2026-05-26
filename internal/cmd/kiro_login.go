package cmd

import (
	"context"
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	log "github.com/sirupsen/logrus"
)

type KiroLoginOptions struct {
	Mode      string
	Import    string
	StartURL  string
	Region    string
	Flow      string
	Incognito bool
}

func DoKiroLogin(cfg *config.Config, options *LoginOptions, kiroOptions KiroLoginOptions) {
	if options == nil {
		options = &LoginOptions{}
	}
	metadata := map[string]string{
		"mode": kiroOptions.Mode,
		"flow": kiroOptions.Flow,
	}
	if kiroOptions.Import != "" {
		metadata["import"] = kiroOptions.Import
	}
	if kiroOptions.StartURL != "" {
		metadata["start-url"] = kiroOptions.StartURL
	}
	if kiroOptions.Region != "" {
		metadata["region"] = kiroOptions.Region
	}
	if kiroOptions.Incognito {
		metadata["incognito"] = "true"
	} else {
		metadata["incognito"] = "false"
	}
	manager := newAuthManager()
	record, savedPath, err := manager.Login(context.Background(), "kiro", cfg, &sdkAuth.LoginOptions{
		NoBrowser: options.NoBrowser,
		Metadata:  metadata,
		Prompt:    options.Prompt,
	})
	if err != nil {
		log.Errorf("Kiro authentication failed: %v", err)
		return
	}
	if savedPath != "" {
		fmt.Printf("Authentication saved to %s\n", savedPath)
	}
	if record != nil && record.Label != "" {
		fmt.Printf("Authenticated as %s\n", record.Label)
	}
	fmt.Println("Kiro authentication successful!")
}
