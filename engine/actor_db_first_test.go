package engine

import (
	"fmt"
	"io"
	"log"
	"net/url"
	"testing"
	"time"

	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/text/language"
	"gorm.io/gorm/logger"

	"github.com/1103020472/metatube-sdk-go/database"
	"github.com/1103020472/metatube-sdk-go/model"
	mt "github.com/1103020472/metatube-sdk-go/provider"
)

type stubActorProvider struct {
	name  string
	calls int
}

func (p *stubActorProvider) Name() string           { return p.name }
func (p *stubActorProvider) Priority() float64      { return 1 }
func (p *stubActorProvider) SetPriority(float64)    {}
func (p *stubActorProvider) Language() language.Tag { return language.Japanese }
func (p *stubActorProvider) URL() *url.URL          { return &url.URL{} }

func (p *stubActorProvider) SearchActor(keyword string) ([]*model.ActorSearchResult, error) {
	p.calls++
	return nil, mt.ErrInfoNotFound
}

func newDBFirstTestEngine(t *testing.T) *Engine {
	t.Helper()
	db, err := database.Open(&database.Config{
		DSN:      fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name()),
		LogLevel: logger.Silent,
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.ActorInfo{}))
	return &Engine{db: db, logger: log.New(io.Discard, "", 0)}
}

func TestSearchActorFromDBFirst(t *testing.T) {
	eng := newDBFirstTestEngine(t)
	provider := &stubActorProvider{name: "AV-LEAGUE"}

	rows := []*model.ActorInfo{
		{
			ID: "1937", Provider: "AV-LEAGUE", Name: "大槻ひびき",
			Homepage:  "https://www.av-league.com/actress/1937.html",
			Images:    pq.StringArray{"https://www.av-league.com/image/act/1937/1937-0.webp"},
			TimeTracker: model.TimeTracker{UpdatedAt: time.Now().Add(-24 * time.Hour)},
		},
		{
			ID: "1", Provider: "AV-LEAGUE", Name: "无图演员",
			Homepage:  "https://www.av-league.com/actress/1.html",
			Images:    pq.StringArray{},
			TimeTracker: model.TimeTracker{UpdatedAt: time.Now().Add(-24 * time.Hour)},
		},
		{
			ID: "2", Provider: "AV-LEAGUE", Name: "过期演员",
			Homepage:  "https://www.av-league.com/actress/2.html",
			Images:    pq.StringArray{"https://www.av-league.com/image/act/2/2-0.webp"},
			TimeTracker: model.TimeTracker{UpdatedAt: time.Now().Add(-400 * 24 * time.Hour)},
		},
	}
	for _, row := range rows {
		require.NoError(t, eng.db.Create(row).Error)
	}

	results, ok := eng.searchActorFromDBFirst("大槻ひびき", provider)
	assert.True(t, ok)
	require.Len(t, results, 1)
	assert.Equal(t, "1937", results[0].ID)
	assert.Equal(t, "AV-LEAGUE", results[0].Provider)

	// Rows without images fall back to a live search so the Gfriends image
	// fallback still works.
	_, ok = eng.searchActorFromDBFirst("无图演员", provider)
	assert.False(t, ok)

	// Rows older than the freshness window are not trusted.
	_, ok = eng.searchActorFromDBFirst("过期演员", provider)
	assert.False(t, ok)

	// Unknown names fall through.
	_, ok = eng.searchActorFromDBFirst("查无此人", provider)
	assert.False(t, ok)

	// Gfriends is cheap enough to always query live.
	_, ok = eng.searchActorFromDBFirst("大槻ひびき", &stubActorProvider{name: "Gfriends"})
	assert.False(t, ok)

	// Provider and name matching are case-insensitive.
	_, ok = eng.searchActorFromDBFirst("大槻ひびき", &stubActorProvider{name: "av-league"})
	assert.True(t, ok)

	require.NoError(t, eng.db.Exec("UPDATE actor_metadata SET name = 'RION' WHERE id = '1937'").Error)
	_, ok = eng.searchActorFromDBFirst("rion", provider)
	assert.True(t, ok)
}

func TestSearchActorUncachedDBFirstSkipsProvider(t *testing.T) {
	eng := newDBFirstTestEngine(t)
	provider := &stubActorProvider{name: "AV-LEAGUE"}

	require.NoError(t, eng.db.Create(&model.ActorInfo{
		ID: "6752", Provider: "AV-LEAGUE", Name: "波多野結衣",
		Homepage:  "https://www.av-league.com/actress/6752.html",
		Images:    pq.StringArray{"https://www.av-league.com/image/act/6752/6752-0.webp"},
		TimeTracker: model.TimeTracker{UpdatedAt: time.Now()},
	}).Error)

	results, err := eng.searchActorUncached("波多野結衣", provider, true, true)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "6752", results[0].ID)
	assert.Zero(t, provider.calls, "DB hit must not query the provider")

	// Without db_first the provider is still queried; the DB row only comes
	// back through the existing fallback merge.
	results, err = eng.searchActorUncached("波多野結衣", provider, true, false)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "6752", results[0].ID)
	assert.Equal(t, 1, provider.calls)
}
