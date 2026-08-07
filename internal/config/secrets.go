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

// SubDLSecrets holds the SubDL Provider's credentials, read from
// environment variables rather than the config file — see
// LoadProviderSecrets. SubDL requires an API key unconditionally, even on
// a free account (ADR 0006), unlike OpenSubtitles' username/password pair.
type SubDLSecrets struct {
	APIKey string
}

// ProviderSecrets holds credentials for every Provider Sublime supports.
type ProviderSecrets struct {
	OpenSubtitles OpenSubtitlesSecrets
	SubDL         SubDLSecrets
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
		SubDL: SubDLSecrets{
			APIKey: os.Getenv("SUBLIME_SUBDL_API_KEY"),
		},
	}
}
