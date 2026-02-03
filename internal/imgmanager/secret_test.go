package imgmanager

import (
	"encoding/base64"
	"maps"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cybozu-go/ofen/internal/constants"
)

func TestConvertCredentials(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		secrets       []corev1.Secret
		expectedCreds map[string]Credentials
		expectError   bool
	}{
		{
			name:          "empty secrets",
			secrets:       []corev1.Secret{},
			expectedCreds: map[string]Credentials{},
			expectError:   false,
		},
		{
			name: "valid dockerconfigjson secret",
			secrets: []corev1.Secret{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "test-secret"},
					Data: map[string][]byte{
						constants.DockerConfigName: []byte(`{
							"auths": {
								"registry.example.com": {
									"auth": "` + base64.StdEncoding.EncodeToString([]byte("user:pass")) + `"
								},
								"registry2.example.com": {
									"auth": "` + base64.StdEncoding.EncodeToString([]byte("user2:pass2")) + `"
								}
							}
						}`),
					},
				},
			},
			expectedCreds: map[string]Credentials{
				"registry.example.com": {
					Username: "user",
					Password: "pass",
				},
				"registry2.example.com": {
					Username: "user2",
					Password: "pass2",
				},
			},
			expectError: false,
		},
		{
			name: "valid dockercfg secret",
			secrets: []corev1.Secret{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "test-secret"},
					Data: map[string][]byte{
						constants.DockerCfgName: []byte(`{
							"registry.example.com": {
								"auth": "` + base64.StdEncoding.EncodeToString([]byte("user:pass")) + `"
							},
							"registry2.example.com": {
								"auth": "` + base64.StdEncoding.EncodeToString([]byte("user2:pass2")) + `"
							}
						}`),
					},
				},
			},
			expectedCreds: map[string]Credentials{
				"registry.example.com": {
					Username: "user",
					Password: "pass",
				},
				"registry2.example.com": {
					Username: "user2",
					Password: "pass2",
				},
			},
			expectError: false,
		},
		{
			name: "multiple secrets with different formats",
			secrets: []corev1.Secret{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "dockerconfigjson-secret"},
					Data: map[string][]byte{
						constants.DockerConfigName: []byte(`{
							"auths": {
								"registry1.example.com": {
									"auth": "` + base64.StdEncoding.EncodeToString([]byte("user1:pass1")) + `"
								}
							}
						}`),
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "dockercfg-secret"},
					Data: map[string][]byte{
						constants.DockerCfgName: []byte(`{
							"registry2.example.com": {
								"auth": "` + base64.StdEncoding.EncodeToString([]byte("user2:pass2")) + `"
							}
						}`),
					},
				},
			},
			expectedCreds: map[string]Credentials{
				"registry1.example.com": {
					Username: "user1",
					Password: "pass1",
				},
				"registry2.example.com": {
					Username: "user2",
					Password: "pass2",
				},
			},
			expectError: false,
		},
		{
			name: "secret without docker config data",
			secrets: []corev1.Secret{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "test-secret"},
					Data: map[string][]byte{
						"other-key": []byte("some-data"),
					},
				},
			},
			expectedCreds: nil,
			expectError:   true,
		},
		{
			name: "invalid dockerconfigjson json",
			secrets: []corev1.Secret{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "test-secret"},
					Data: map[string][]byte{
						constants.DockerConfigName: []byte(`invalid json`),
					},
				},
			},
			expectedCreds: nil,
			expectError:   true,
		},
		{
			name: "invalid dockercfg json",
			secrets: []corev1.Secret{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "test-secret"},
					Data: map[string][]byte{
						constants.DockerCfgName: []byte(`invalid json`),
					},
				},
			},
			expectedCreds: nil,
			expectError:   true,
		},
		{
			name: "invalid base64 in dockerconfigjson",
			secrets: []corev1.Secret{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "test-secret"},
					Data: map[string][]byte{
						constants.DockerConfigName: []byte(`{
							"auths": {
								"registry.example.com": {
									"auth": "invalid-base64!"
								}
							}
						}`),
					},
				},
			},
			expectedCreds: nil,
			expectError:   true,
		},
		{
			name: "invalid base64 in dockercfg",
			secrets: []corev1.Secret{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "test-secret"},
					Data: map[string][]byte{
						constants.DockerCfgName: []byte(`{
							"registry.example.com": {
								"auth": "invalid-base64!"
							}
						}`),
					},
				},
			},
			expectedCreds: nil,
			expectError:   true,
		},
		{
			name: "invalid auth format without colon in dockerconfigjson",
			secrets: []corev1.Secret{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "test-secret"},
					Data: map[string][]byte{
						constants.DockerConfigName: []byte(`{
							"auths": {
								"registry.example.com": {
									"auth": "` + base64.StdEncoding.EncodeToString([]byte("nocolon")) + `"
								}
							}
						}`),
					},
				},
			},
			expectedCreds: nil,
			expectError:   true,
		},
		{
			name: "invalid auth format without colon in dockercfg",
			secrets: []corev1.Secret{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "test-secret"},
					Data: map[string][]byte{
						constants.DockerCfgName: []byte(`{
							"registry.example.com": {
								"auth": "` + base64.StdEncoding.EncodeToString([]byte("nocolon")) + `"
							}
						}`),
					},
				},
			},
			expectedCreds: nil,
			expectError:   true,
		},
		{
			name: "dockerconfigjson takes precedence over dockercfg",
			secrets: []corev1.Secret{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "test-secret"},
					Data: map[string][]byte{
						constants.DockerConfigName: []byte(`{
							"auths": {
								"registry.example.com": {
									"auth": "` + base64.StdEncoding.EncodeToString([]byte("user:pass")) + `"
								}
							}
						}`),
						constants.DockerCfgName: []byte(`{
							"registry.example.com": {
								"auth": "` + base64.StdEncoding.EncodeToString([]byte("otheruser:otherpass")) + `"
							}
						}`),
					},
				},
			},
			expectedCreds: map[string]Credentials{
				"registry.example.com": {
					Username: "user",
					Password: "pass",
				},
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			creds, err := convertCredentials(tt.secrets)
			if tt.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, tt.expectedCreds, creds)
			}
		})
	}
}

func TestProcessDockerConfigJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		data          []byte
		initialTokens map[string]Credentials
		expectedCreds map[string]Credentials
		expectError   bool
	}{
		{
			name: "valid data",
			data: []byte(`{
				"auths": {
					"registry.example.com": {
						"auth": "` + base64.StdEncoding.EncodeToString([]byte("user:pass")) + `"
					}
				}
			}`),
			initialTokens: map[string]Credentials{},
			expectedCreds: map[string]Credentials{
				"registry.example.com": {
					Username: "user",
					Password: "pass",
				},
			},
			expectError: false,
		},
		{
			name:          "invalid json",
			data:          []byte(`invalid json`),
			initialTokens: map[string]Credentials{},
			expectedCreds: nil,
			expectError:   true,
		},
		{
			name: "invalid auth",
			data: []byte(`{
				"auths": {
					"registry.example.com": {
						"auth": "invalid-base64!"
					}
				}
			}`),
			initialTokens: map[string]Credentials{},
			expectedCreds: nil,
			expectError:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tokens := make(map[string]Credentials)
			maps.Copy(tokens, tt.initialTokens)

			err := processDockerConfigJSON(tt.data, tokens)
			if tt.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, tt.expectedCreds, tokens)
			}
		})
	}
}

func TestProcessDockerCfg(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		data          []byte
		initialTokens map[string]Credentials
		expectedCreds map[string]Credentials
		expectError   bool
	}{
		{
			name: "valid data",
			data: []byte(`{
				"registry.example.com": {
					"auth": "` + base64.StdEncoding.EncodeToString([]byte("user:pass")) + `"
				}
			}`),
			initialTokens: map[string]Credentials{},
			expectedCreds: map[string]Credentials{
				"registry.example.com": {
					Username: "user",
					Password: "pass",
				},
			},
			expectError: false,
		},
		{
			name:          "invalid json",
			data:          []byte(`invalid json`),
			initialTokens: map[string]Credentials{},
			expectedCreds: nil,
			expectError:   true,
		},
		{
			name: "invalid auth",
			data: []byte(`{
				"registry.example.com": {
					"auth": "invalid-base64!"
				}
			}`),
			initialTokens: map[string]Credentials{},
			expectedCreds: nil,
			expectError:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tokens := make(map[string]Credentials)
			maps.Copy(tokens, tt.initialTokens)

			err := processDockerCfg(tt.data, tokens)
			if tt.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, tt.expectedCreds, tokens)
			}
		})
	}
}

func TestExtractCredentials(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		registry      string
		authString    string
		initialTokens map[string]Credentials
		expectedCreds map[string]Credentials
		expectError   bool
	}{
		{
			name:          "valid auth",
			registry:      "registry.example.com",
			authString:    base64.StdEncoding.EncodeToString([]byte("user:pass")),
			initialTokens: map[string]Credentials{},
			expectedCreds: map[string]Credentials{
				"registry.example.com": {
					Username: "user",
					Password: "pass",
				},
			},
			expectError: false,
		},
		{
			name:          "auth with multiple colons",
			registry:      "registry.example.com",
			authString:    base64.StdEncoding.EncodeToString([]byte("user:pass:word")),
			initialTokens: map[string]Credentials{},
			expectedCreds: map[string]Credentials{
				"registry.example.com": {
					Username: "user",
					Password: "pass:word",
				},
			},
			expectError: false,
		},
		{
			name:          "empty password",
			registry:      "registry.example.com",
			authString:    base64.StdEncoding.EncodeToString([]byte("user:")),
			initialTokens: map[string]Credentials{},
			expectedCreds: map[string]Credentials{
				"registry.example.com": {
					Username: "user",
					Password: "",
				},
			},
			expectError: false,
		},
		{
			name:          "invalid base64",
			registry:      "registry.example.com",
			authString:    "invalid-base64!",
			initialTokens: map[string]Credentials{},
			expectedCreds: nil,
			expectError:   true,
		},
		{
			name:          "no colon in auth",
			registry:      "registry.example.com",
			authString:    base64.StdEncoding.EncodeToString([]byte("nocolon")),
			initialTokens: map[string]Credentials{},
			expectedCreds: nil,
			expectError:   true,
		},
		{
			name:       "add to existing tokens",
			registry:   "registry2.example.com",
			authString: base64.StdEncoding.EncodeToString([]byte("user2:pass2")),
			initialTokens: map[string]Credentials{
				"registry1.example.com": {
					Username: "user1",
					Password: "pass1",
				},
			},
			expectedCreds: map[string]Credentials{
				"registry1.example.com": {
					Username: "user1",
					Password: "pass1",
				},
				"registry2.example.com": {
					Username: "user2",
					Password: "pass2",
				},
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tokens := make(map[string]Credentials)
			maps.Copy(tokens, tt.initialTokens)

			err := extractCredentials(tt.registry, tt.authString, tokens)
			if tt.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, tt.expectedCreds, tokens)
			}
		})
	}
}
