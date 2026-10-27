package provider

import (
	"errors"
	"iter"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"golang.org/x/text/language"

	"github.com/1103020472/metatube-sdk-go/model"
)

// ErrKeyNotFound is returned by EditableConfig getters for missing keys.
var ErrKeyNotFound = errors.New("key not found")

type Provider interface {
	// Name returns the name of the provider.
	Name() string

	// Priority returns the matching priority of the provider.
	Priority() float64

	// SetPriority sets the provider priority to the given value.
	SetPriority(v float64)

	// Language returns the primary language supported by the provider.
	Language() language.Tag

	// URL returns the base url of the provider.
	URL() *url.URL
}

type MovieSearcher interface {
	// SearchMovie searches matched movies.
	SearchMovie(keyword string) ([]*model.MovieSearchResult, error)

	// NormalizeMovieKeyword converts movie keyword to provider-friendly form.
	NormalizeMovieKeyword(Keyword string) string
}

type MovieReviewer interface {
	// GetMovieReviewsByID gets the user reviews of given movie id.
	GetMovieReviewsByID(id string) ([]*model.MovieReviewDetail, error)

	// GetMovieReviewsByURL gets the user reviews of given movie URL.
	GetMovieReviewsByURL(rawURL string) ([]*model.MovieReviewDetail, error)
}

type MovieProvider interface {
	// Provider should be implemented.
	Provider

	// NormalizeMovieID normalizes movie ID to conform to standard.
	NormalizeMovieID(id string) string

	// ParseMovieIDFromURL parses movie ID from given URL.
	ParseMovieIDFromURL(rawURL string) (string, error)

	// GetMovieInfoByID gets movie's info by id.
	GetMovieInfoByID(id string) (*model.MovieInfo, error)

	// GetMovieInfoByURL gets movie's info by url.
	GetMovieInfoByURL(url string) (*model.MovieInfo, error)
}

// MovieNumberProvider is implemented by movie providers that can resolve a
// movie number (e.g. "SSIS-001") into movie info directly. JavDB is such a
// provider: its IDs are short hashes, so a number is resolved by searching.
type MovieNumberProvider interface {
	// GetMovieInfoByNumber gets movie's info by its number.
	GetMovieInfoByNumber(number string) (*model.MovieInfo, error)
}

// ActorFallbackSetter is implemented by providers that borrow the actor list
// from another provider when their own source has none.
type ActorFallbackSetter interface {
	// SetActorFallbackProvider sets the fallback source of actors.
	SetActorFallbackProvider(provider MovieNumberProvider)
}

type ActorSearcher interface {
	// SearchActor searches matched actor/s.
	SearchActor(keyword string) ([]*model.ActorSearchResult, error)
}

type ActorProvider interface {
	// Provider should be implemented.
	Provider

	// NormalizeActorID normalizes actor ID to conform to standard.
	NormalizeActorID(id string) string

	// ParseActorIDFromURL parses actor ID from given URL.
	ParseActorIDFromURL(rawURL string) (string, error)

	// GetActorInfoByID gets actor's info by id.
	GetActorInfoByID(id string) (*model.ActorInfo, error)

	// GetActorInfoByURL gets actor's info by url.
	GetActorInfoByURL(url string) (*model.ActorInfo, error)
}

type Fetcher interface {
	// Fetch fetches media resources from url.
	Fetch(url string) (*http.Response, error)
}

type RequestTimeoutSetter interface {
	// SetRequestTimeout sets timeout for HTTP requests.
	SetRequestTimeout(timeout time.Duration)
}

type ProxySetter interface {
	// SetProxy sets http or socks5 proxy for HTTP requests.
	SetProxy(proxyURL string) error
}

// ExtraHostsProvider is implemented by providers that also serve content
// from hostnames other than their base URL host. The engine registers
// these hosts so that URLs pointing to them route back to the provider.
type ExtraHostsProvider interface {
	// ExtraHosts returns additional hostnames (without scheme, port optional).
	ExtraHosts() []string
}

type Config interface {
	Has(string) bool
	GetString(string) (string, error)
	GetBool(string) (bool, error)
	GetInt64(string) (int64, error)
	GetFloat64(string) (float64, error)
	GetDuration(string) (time.Duration, error)
}

type ConfigSetter interface {
	// SetConfig sets any additional configs for Provider.
	SetConfig(config Config) error
}

// EditableConfig is a Config that can be built at runtime, used by the
// engine's runtime provider-config API.
type EditableConfig interface {
	Config

	Set(key, value string)
	Delete(key string)
	Len() int
	Iterator() iter.Seq2[string, string]
}

// NewEditableConfig returns an empty EditableConfig.
func NewEditableConfig() EditableConfig {
	return &editableConfig{values: make(map[string]string)}
}

type editableConfig struct {
	values map[string]string
}

func (c *editableConfig) Set(key, value string) { c.values[key] = value }
func (c *editableConfig) Delete(key string)     { delete(c.values, key) }
func (c *editableConfig) Len() int              { return len(c.values) }
func (c *editableConfig) Has(key string) bool   { _, ok := c.values[key]; return ok }
func (c *editableConfig) GetString(key string) (string, error) {
	v, ok := c.values[key]
	if !ok {
		return "", ErrKeyNotFound
	}
	return v, nil
}
func (c *editableConfig) GetBool(key string) (bool, error) {
	v, ok := c.values[key]
	if !ok {
		return false, ErrKeyNotFound
	}
	return strconv.ParseBool(v)
}
func (c *editableConfig) GetInt64(key string) (int64, error) {
	v, ok := c.values[key]
	if !ok {
		return 0, ErrKeyNotFound
	}
	return strconv.ParseInt(v, 10, 64)
}
func (c *editableConfig) GetFloat64(key string) (float64, error) {
	v, ok := c.values[key]
	if !ok {
		return 0, ErrKeyNotFound
	}
	return strconv.ParseFloat(v, 64)
}
func (c *editableConfig) GetDuration(key string) (time.Duration, error) {
	v, ok := c.values[key]
	if !ok {
		return 0, ErrKeyNotFound
	}
	return time.ParseDuration(v)
}

// Iterator returns all key-value pairs, used by callers that need to copy
// the config into another implementation.
func (c *editableConfig) Iterator() iter.Seq2[string, string] {
	return func(yield func(string, string) bool) {
		for k, v := range c.values {
			if !yield(k, v) {
				return
			}
		}
	}
}
