package engine

import (
	mt "github.com/1103020472/metatube-sdk-go/provider"
)

// GetMovieProviderConfig returns the runtime config of a movie provider.
// The second return value reports whether any config exists.
//
// The map merges the env-based config (MT_MOVIE_PROVIDER_*) captured at init
// time with any later runtime updates, so the caller always sees the values
// currently in effect.
func (e *Engine) GetMovieProviderConfig(name string) (mt.EditableConfig, bool) {
	merged := mt.NewEditableConfig()
	if init, ok := e.initMovieProviderConfigs.Get(name); ok {
		if editable, ok := init.(mt.EditableConfig); ok {
			for k, v := range editable.Iterator() {
				merged.Set(k, v)
			}
		}
	}
	if runtime, ok := e.movieProviderConfigs.Get(name); ok {
		if editable, ok := runtime.(mt.EditableConfig); ok {
			for k, v := range editable.Iterator() {
				merged.Set(k, v)
			}
		}
	}
	return merged, merged.Len() > 0
}

// SetMovieProviderConfig updates a movie provider's config at runtime,
// without restarting the server. The config is kept in movieProviderConfigs
// so that it survives subsequent GetMovieProviderConfig calls.
//
// The following keys are honored (same as init time):
//   - proxy:   http/socks5 proxy url (provider.ProxySetter)
//   - timeout: per-request timeout   (provider.RequestTimeoutSetter)
//   - others:  passed to provider.ConfigSetter as is (e.g. JavDB's "cookie")
func (e *Engine) SetMovieProviderConfig(name string, config mt.Config) error {
	provider, err := e.GetMovieProviderByName(name)
	if err != nil {
		return err
	}

	if s, ok := provider.(mt.ProxySetter); ok && config.Has("proxy") {
		if v, err := config.GetString("proxy"); err == nil {
			if err := s.SetProxy(v); err != nil {
				return err
			}
		}
	}

	if s, ok := provider.(mt.RequestTimeoutSetter); ok && config.Has("timeout") {
		if v, err := config.GetDuration("timeout"); err == nil {
			s.SetRequestTimeout(v)
		}
	}

	if s, ok := provider.(mt.ConfigSetter); ok {
		if err := s.SetConfig(config); err != nil {
			return err
		}
	}

	e.movieProviderConfigs.Set(name, config)
	e.logger.Printf("Update movie provider config: %s", name)
	return nil
}
