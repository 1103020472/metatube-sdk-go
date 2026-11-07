package engine

import (
	"time"

	"github.com/jellydator/ttlcache/v3"

	"github.com/1103020472/metatube-sdk-go/model"
)

const (
	// actorSearchCacheTTL is fixed: every entry expires 6 hours after it was
	// stored. Actor metadata changes slowly, so this is long enough to absorb
	// repeated scraping while still letting corrections show up the same day.
	actorSearchCacheTTL = 6 * time.Hour

	// actorSearchCacheCapacity bounds memory usage for long-running servers.
	actorSearchCacheCapacity = 5000
)

type actorSearchCacheKey struct {
	keyword  string
	provider string
	fallback bool
}

// actorSearchCache caches successful, non-empty actor search results.
//
// Actor search is the most expensive step of scraping: every miss hits the
// provider's website. The same actor is queried many times during a single
// library scan, so caching cuts most of those remote calls.
//
// Empty results and errors are deliberately not cached: "actor not found" may
// be a transient provider failure, and pinning that for 6 hours would hide the
// real problem.
type actorSearchCache struct {
	cache *ttlcache.Cache[actorSearchCacheKey, []*model.ActorSearchResult]
}

func newActorSearchCache() *actorSearchCache {
	return &actorSearchCache{
		cache: ttlcache.New[actorSearchCacheKey, []*model.ActorSearchResult](
			ttlcache.WithTTL[actorSearchCacheKey, []*model.ActorSearchResult](actorSearchCacheTTL),
			ttlcache.WithCapacity[actorSearchCacheKey, []*model.ActorSearchResult](actorSearchCacheCapacity),
		),
	}
}

func (c *actorSearchCache) Get(keyword, provider string, fallback bool) ([]*model.ActorSearchResult, bool) {
	if c == nil {
		return nil, false
	}
	item := c.cache.Get(actorSearchCacheKey{
		keyword:  keyword,
		provider: provider,
		fallback: fallback,
	})
	if item == nil {
		return nil, false
	}
	// Return a copy: callers append/sort their own result slices.
	results := item.Value()
	return append([]*model.ActorSearchResult(nil), results...), true
}

func (c *actorSearchCache) Put(keyword, provider string, fallback bool, results []*model.ActorSearchResult) {
	if c == nil || len(results) == 0 {
		return
	}
	c.cache.Set(actorSearchCacheKey{
		keyword:  keyword,
		provider: provider,
		fallback: fallback,
	}, append([]*model.ActorSearchResult(nil), results...), ttlcache.DefaultTTL)
}

func (c *actorSearchCache) Clear() {
	if c != nil {
		c.cache.DeleteAll()
	}
}
