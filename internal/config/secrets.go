package config

import "os"

// OpenSubtitlesSecrets holds the OpenSubtitles Provider's credentials, read
// from environment variables rather than the config file — see
// LoadProviderSecrets.
type OpenSubtitlesSecrets struct {
	APIKey   string
	Username string
	Password string
}

// ProviderSecrets holds credentials for every Provider Sublime supports.
// Sublime supports one Provider (OpenSubtitles) in v1, but this is
// structured to add more without a breaking change.
type ProviderSecrets struct {
	OpenSubtitles OpenSubtitlesSecrets
}

// LoadProviderSecrets reads Provider credentials from their designated
// SUBLIME_<PROVIDER>_<CREDENTIAL> environment variables. Provider secrets
// are never read from the config file.
func LoadProviderSecrets() ProviderSecrets {
	return ProviderSecrets{
		OpenSubtitles: OpenSubtitlesSecrets{
			APIKey:   os.Getenv("SUBLIME_OPENSUBTITLES_API_KEY"),
			Username: os.Getenv("SUBLIME_OPENSUBTITLES_USERNAME"),
			Password: os.Getenv("SUBLIME_OPENSUBTITLES_PASSWORD"),
		},
	}
}
