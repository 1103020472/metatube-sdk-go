package engine

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/1103020472/metatube-sdk-go/model"
)

func TestActorSearchCache(t *testing.T) {
	cache := newActorSearchCache()
	results := []*model.ActorSearchResult{
		{ID: "1", Name: "上原亜衣", Provider: "av-league"},
	}

	cache.Put("上原亜衣", "av-league", true, results)

	got, ok := cache.Get("上原亜衣", "av-league", true)
	assert.True(t, ok)
	assert.Equal(t, results, got)

	// Fallback and provider are part of the key.
	_, ok = cache.Get("上原亜衣", "av-league", false)
	assert.False(t, ok)
	_, ok = cache.Get("上原亜衣", "gfriends", true)
	assert.False(t, ok)

	// Empty results are never cached.
	cache.Put("unknown", "av-league", true, nil)
	_, ok = cache.Get("unknown", "av-league", true)
	assert.False(t, ok)

	// Returned slices are copies, so callers can append safely.
	got = append(got, &model.ActorSearchResult{ID: "2"})
	again, ok := cache.Get("上原亜衣", "av-league", true)
	assert.True(t, ok)
	assert.Len(t, again, 1)
	assert.Len(t, got, 2)
}
