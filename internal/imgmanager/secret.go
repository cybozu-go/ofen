package imgmanager

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"

	"github.com/cybozu-go/ofen/internal/constants"
)

type DockerConfig struct {
	Auths map[string]struct {
		Auth string `json:"auth"`
	} `json:"auths"`
}

type DockerCfg map[string]struct {
	Auth string `json:"auth"`
}

type Credentials struct {
	Username string
	Password string
}

func convertCredentials(secrets []corev1.Secret) (map[string]Credentials, error) {
	tokens := map[string]Credentials{}
	for _, secret := range secrets {
		if data, exists := secret.Data[constants.DockerConfigName]; exists {
			if err := processDockerConfigJSON(data, tokens); err != nil {
				return tokens, fmt.Errorf("failed to process dockerconfigjson: %w", err)
			}
			continue
		}

		if data, exists := secret.Data[constants.DockerCfgName]; exists {
			if err := processDockerCfg(data, tokens); err != nil {
				return tokens, fmt.Errorf("failed to process dockercfg: %w", err)
			}
			continue
		}

		return tokens, fmt.Errorf("secret does not contain %s or %s data", constants.DockerConfigName, constants.DockerCfgName)
	}

	return tokens, nil
}

func processDockerConfigJSON(data []byte, tokens map[string]Credentials) error {
	var dockerConfig DockerConfig
	if err := json.Unmarshal(data, &dockerConfig); err != nil {
		return fmt.Errorf("failed to unmarshal dockerconfigjson data: %w", err)
	}

	for registry, auth := range dockerConfig.Auths {
		if err := extractCredentials(registry, auth.Auth, tokens); err != nil {
			return err
		}
	}
	return nil
}

func processDockerCfg(data []byte, tokens map[string]Credentials) error {
	var dockerCfg DockerCfg
	if err := json.Unmarshal(data, &dockerCfg); err != nil {
		return fmt.Errorf("failed to unmarshal dockercfg data: %w", err)
	}

	for registry, auth := range dockerCfg {
		if err := extractCredentials(registry, auth.Auth, tokens); err != nil {
			return err
		}
	}
	return nil
}

func extractCredentials(registry, authString string, tokens map[string]Credentials) error {
	data, err := base64.StdEncoding.DecodeString(authString)
	if err != nil {
		return fmt.Errorf("failed to decode auth %s: %w", authString, err)
	}

	username, password, ok := strings.Cut(string(data), ":")
	if !ok {
		return fmt.Errorf("failed to find username and password in auth %s", authString)
	}

	tokens[registry] = Credentials{
		Username: username,
		Password: password,
	}
	return nil
}
