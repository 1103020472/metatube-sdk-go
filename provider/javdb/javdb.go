package javdb

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gocolly/colly/v2"
	"golang.org/x/text/language"

	"github.com/1103020472/metatube-sdk-go/common/fetch"
	"github.com/1103020472/metatube-sdk-go/common/parser"
	sdkerrors "github.com/1103020472/metatube-sdk-go/errors"
	"github.com/1103020472/metatube-sdk-go/model"
	"github.com/1103020472/metatube-sdk-go/provider"
	"github.com/1103020472/metatube-sdk-go/provider/internal/scraper"
)

var (
	_ provider.MovieProvider = (*JavDB)(nil)
	_ provider.MovieSearcher = (*JavDB)(nil)
	_ provider.Fetcher       = (*JavDB)(nil)
	_ provider.ConfigSetter  = (*JavDB)(nil)
)

const (
	Name     = "JavDB"
	Priority = 1000 - 4
)

const (
	baseURL   = "https://javdb.com/"
	movieURL  = "https://javdb.com/v/%s"
	searchURL = "https://javdb.com/search?q=%s&f=all"
)

// JavDB identifies a movie by a short hash in its URL
// (https://javdb.com/v/Mb4Dbv) instead of by the movie number.
var movieIDRegexp = regexp.MustCompile(`(?i)^/v/([a-z\d]+)`)

// JavDB requires a dash in the VR serial numbers: "3DSVR00123" finds nothing,
// while "DSVR-123" matches. Jellyfin's plugin converts "DSVR-123" into
// "3DSVR00123" before searching, so translate it back here.
var vrNumberRegexp = regexp.MustCompile(`^3?(DSVR|VRKM|SAVR)[-_]?(\d+)$`)

var scoreRegexp = regexp.MustCompile(`(\d+(?:\.\d+)?)\s*分`)

var nonAlnumRegexp = regexp.MustCompile(`[^A-Z\d]`)

// errCloudflare is returned when Cloudflare refuses to serve real content,
// which usually means the request needs to go through a proxy.
var errCloudflare = sdkerrors.New(
	http.StatusForbidden,
	"javdb: request blocked by Cloudflare challenge, configure a proxy for this provider",
)

// errLoginRequired is returned for entries whose detail page is only visible
// to logged-in JavDB users, e.g. most FC2 and amateur entries. Such movies can
// still be found by search, but only a session cookie unlocks their details.
var errLoginRequired = sdkerrors.New(
	http.StatusUnauthorized,
	"javdb: entry details require a JavDB account, configure a session cookie for this provider",
)

type JavDB struct {
	*fetch.Fetcher
	*scraper.Scraper

	// cookie is an optional JavDB session cookie.
	cookie atomic.Value // string
}

func New() *JavDB {
	return &JavDB{
		Fetcher: fetch.Default(&fetch.Config{Referer: baseURL}),
		Scraper: scraper.NewDefaultScraper(
			Name, baseURL, Priority,
			language.Chinese,
			scraper.WithHeaders(map[string]string{
				"Referer":         baseURL,
				"Accept-Language": "zh-TW,zh;q=0.9,ja;q=0.8,en;q=0.7",
			}),
			// JavDB sits behind Cloudflare, keep the request rate low.
			scraper.WithLimit(&colly.LimitRule{
				DomainGlob:  "*",
				Parallelism: 2,
				Delay:       300 * time.Millisecond,
			}),
		),
	}
}

// SetConfig accepts an extra "cookie" value, e.g.
// MT_MOVIE_PROVIDER_JAVDB__COOKIE="_jdb_session=...", which unlocks the
// login-only entries (most FC2 and amateur ones).
func (javdb *JavDB) SetConfig(config provider.Config) error {
	if value, err := config.GetString("cookie"); err == nil {
		if value = strings.TrimSpace(value); value != "" {
			javdb.cookie.Store(value)
		}
	}
	return nil
}

// collector returns a cloned collector with the configured session cookie,
// if any, attached to every request.
func (javdb *JavDB) collector() *colly.Collector {
	c := javdb.ClonedCollector()
	c.ParseHTTPErrorResponse = true
	if cookie, _ := javdb.cookie.Load().(string); cookie != "" {
		c.OnRequest(func(r *colly.Request) {
			r.Headers.Set("Cookie", cookie)
		})
	}
	return c
}

func (javdb *JavDB) NormalizeMovieID(id string) string {
	return strings.TrimSpace(id)
}

func (javdb *JavDB) ParseMovieIDFromURL(rawURL string) (string, error) {
	homepage, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	if ss := movieIDRegexp.FindStringSubmatch(homepage.Path); len(ss) == 2 {
		return ss[1], nil
	}
	return "", provider.ErrInvalidURL
}

func (javdb *JavDB) GetMovieInfoByID(id string) (*model.MovieInfo, error) {
	id = javdb.NormalizeMovieID(id)
	if id == "" {
		return nil, provider.ErrInvalidID
	}

	// JavDB movie IDs are hashes, movie numbers are resolved through search.
	info, err := javdb.GetMovieInfoByURL(fmt.Sprintf(movieURL, url.PathEscape(id)))
	if err == nil {
		return info, nil
	}
	if !isNotFound(err) {
		return nil, err
	}
	return javdb.GetMovieInfoByNumber(id)
}

// GetMovieInfoByNumber resolves a movie number (e.g. "IPVR-328") into movie
// info by searching JavDB and picking the exact match, if any.
func (javdb *JavDB) GetMovieInfoByNumber(number string) (*model.MovieInfo, error) {
	keyword := javdb.NormalizeMovieKeyword(number)
	if keyword == "" {
		return nil, provider.ErrInvalidID
	}

	results, err := javdb.SearchMovie(keyword)
	if err != nil {
		return nil, err
	}

	wanted := map[string]struct{}{
		normalizeNumber(number):  {},
		normalizeNumber(keyword): {},
	}
	for _, result := range results {
		if _, ok := wanted[normalizeNumber(result.Number)]; ok {
			return javdb.GetMovieInfoByURL(result.Homepage)
		}
	}
	return nil, provider.ErrInfoNotFound
}

func (javdb *JavDB) GetMovieInfoByURL(rawURL string) (info *model.MovieInfo, err error) {
	id, err := javdb.ParseMovieIDFromURL(rawURL)
	if err != nil {
		return nil, err
	}

	info = &model.MovieInfo{
		ID:            id,
		Provider:      javdb.Name(),
		Homepage:      rawURL,
		Actors:        []string{},
		PreviewImages: []string{},
		Genres:        []string{},
	}

	c := javdb.collector()

	var reqErr error
	c.OnResponse(func(r *colly.Response) {
		reqErr = responseError(r)
	})

	// Title, JavDB shows a localized title but keeps the original one in a
	// hidden span. Prefer the original so that it stays consistent with the
	// search results and with the other providers, and let the caller handle
	// the localization.
	c.OnXML(`//h2[contains(@class, "title")]`, func(e *colly.XMLElement) {
		title := strings.TrimSpace(e.ChildText(`.//strong[@class="current-title"]`))
		if origin := strings.TrimSpace(e.ChildText(`.//span[@class="origin-title"]`)); origin != "" {
			title = origin
		}
		info.Title = title
	})

	// Cover
	c.OnXML(`//div[contains(@class, "column-video-cover")]//img[@class="video-cover"]`, func(e *colly.XMLElement) {
		if src := e.Attr("src"); src != "" {
			info.CoverURL = e.Request.AbsoluteURL(src)
		}
	})

	// Number, ReleaseDate, Runtime & Score live in the info panel.
	c.OnXML(`//nav[@class="panel movie-panel-info"]//*[contains(@class, "panel-block")]`, func(e *colly.XMLElement) {
		label := strings.TrimRight(strings.TrimSpace(e.ChildText(`.//strong`)), ":：")
		value := e.ChildText(`.//span[@class="value"]`)
		switch label {
		case "番號":
			if number := strings.ToUpper(strings.TrimSpace(value)); number != "" {
				info.Number = number
			}
		case "日期":
			info.ReleaseDate = parser.ParseDate(value)
		case "時長":
			info.Runtime = parser.ParseRuntime(value)
		case "評分":
			info.Score = parseScore(value)
		}
	})

	// Director, Maker & Series are the only links of their kind in the panel.
	c.OnXML(`//nav[@class="panel movie-panel-info"]//a[starts-with(@href, "/directors/")]`, func(e *colly.XMLElement) {
		info.Director = strings.TrimSpace(e.Text)
	})
	c.OnXML(`//nav[@class="panel movie-panel-info"]//a[starts-with(@href, "/makers/")]`, func(e *colly.XMLElement) {
		info.Maker = strings.TrimSpace(e.Text)
	})
	c.OnXML(`//nav[@class="panel movie-panel-info"]//a[starts-with(@href, "/series/")]`, func(e *colly.XMLElement) {
		info.Series = strings.TrimSpace(e.Text)
	})

	// Genres, JavDB may repeat the same tag (e.g. "VR"), so deduplicate them.
	seenGenres := make(map[string]struct{})
	c.OnXML(`//nav[@class="panel movie-panel-info"]//a[starts-with(@href, "/tags")]`, func(e *colly.XMLElement) {
		genre := strings.TrimSpace(e.Text)
		if genre == "" {
			return
		}
		if _, ok := seenGenres[genre]; !ok {
			seenGenres[genre] = struct{}{}
			info.Genres = append(info.Genres, genre)
		}
	})

	// Actors
	c.OnXML(`//nav[@class="panel movie-panel-info"]//a[starts-with(@href, "/actors/")]`, func(e *colly.XMLElement) {
		if actor := strings.TrimSpace(e.Text); actor != "" {
			info.Actors = append(info.Actors, actor)
		}
	})

	// Previews
	c.OnXML(`//*[contains(@class, "preview-images")]/a[@class="tile-item"]`, func(e *colly.XMLElement) {
		if href := e.Attr("href"); href != "" {
			info.PreviewImages = append(info.PreviewImages, e.Request.AbsoluteURL(href))
		}
	})

	// Thumb, JavDB serves the same asset for cover and thumb.
	c.OnScraped(func(_ *colly.Response) {
		if info.ThumbURL == "" {
			info.ThumbURL = info.CoverURL
		}
	})

	if err = c.Visit(info.Homepage); err != nil {
		return nil, err
	}
	if reqErr != nil {
		return nil, reqErr
	}
	if info.Title == "" || info.CoverURL == "" {
		// Not a movie page, e.g. JavDB redirected to the homepage.
		return nil, provider.ErrInfoNotFound
	}
	return info, nil
}

func (javdb *JavDB) NormalizeMovieKeyword(keyword string) string {
	keyword = strings.ToUpper(strings.TrimSpace(keyword))
	if ss := vrNumberRegexp.FindStringSubmatch(keyword); len(ss) == 3 {
		return fmt.Sprintf("%s-%s", ss[1], strings.TrimLeft(ss[2], "0"))
	}
	return keyword
}

func (javdb *JavDB) SearchMovie(keyword string) (results []*model.MovieSearchResult, err error) {
	c := javdb.collector()

	var reqErr error
	c.OnResponse(func(r *colly.Response) {
		reqErr = responseError(r)
	})

	c.OnXML(`//div[contains(@class, "movie-list")]/div[@class="item"]/a[@class="box"]`, func(e *colly.XMLElement) {
		homepage := e.Request.AbsoluteURL(e.Attr("href"))
		id, idErr := javdb.ParseMovieIDFromURL(homepage)
		if idErr != nil {
			return
		}

		number := strings.ToUpper(strings.TrimSpace(e.ChildText(`.//div[@class="video-title"]/strong`)))
		title := strings.TrimSpace(e.Attr("title"))
		if title == "" {
			// Fallback, strip the leading number from the title element.
			title = strings.TrimSpace(strings.TrimPrefix(
				e.ChildText(`.//div[@class="video-title"]`), number))
		}

		var thumb string
		if src := e.ChildAttr(`.//div[contains(@class, "cover")]/img`, "src"); src != "" {
			thumb = e.Request.AbsoluteURL(src)
		}
		results = append(results, &model.MovieSearchResult{
			ID:       id,
			Number:   number,
			Title:    title,
			Provider: javdb.Name(),
			Homepage: homepage,
			ThumbURL: thumb,
			CoverURL: thumb,
			Score: parseScore(
				e.ChildText(`.//div[@class="score"]/span[@class="value"]`)),
			ReleaseDate: parser.ParseDate(
				e.ChildText(`.//div[@class="meta"]`)),
		})
	})

	if err = c.Visit(fmt.Sprintf(searchURL, url.QueryEscape(keyword))); err != nil {
		return nil, err
	}
	if reqErr != nil {
		return nil, reqErr
	}
	return results, nil
}

func init() {
	provider.Register(Name, New)
}

// responseError converts a JavDB response into a meaningful error, so that
// callers never mistake a Cloudflare challenge page for real content.
func responseError(r *colly.Response) error {
	switch {
	case r == nil:
		return nil
	case isCloudflareChallenge(r):
		return errCloudflare
	case isLoginRedirect(r):
		return errLoginRequired
	case r.StatusCode == http.StatusNotFound:
		return provider.ErrInfoNotFound
	case r.StatusCode != http.StatusOK:
		return sdkerrors.FromCode(r.StatusCode)
	}
	return nil
}

// isLoginRedirect reports whether the request ended up on JavDB's login page,
// which is how JavDB hides the details of login-only entries.
func isLoginRedirect(r *colly.Response) bool {
	return r != nil && r.Request != nil && r.Request.URL != nil &&
		strings.HasPrefix(r.Request.URL.Path, "/login")
}

// isCloudflareChallenge reports whether the response is a Cloudflare
// interstitial or block page instead of the requested content.
func isCloudflareChallenge(r *colly.Response) bool {
	if r == nil {
		return false
	}
	if strings.EqualFold(r.Headers.Get("Cf-Mitigated"), "challenge") {
		return true
	}
	switch r.StatusCode {
	case http.StatusForbidden, http.StatusTooManyRequests, http.StatusServiceUnavailable:
		return true
	}
	if r.StatusCode != http.StatusOK {
		return false
	}
	// Challenge pages are served with 200 as well.
	for _, marker := range [][]byte{
		[]byte("Just a moment"),
		[]byte("_cf_chl_opt"),
		[]byte("cf-challenge"),
		[]byte("Enable JavaScript and cookies to continue"),
		[]byte("Attention Required! | Cloudflare"),
		[]byte("Sorry, you have been blocked"),
	} {
		if bytes.Contains(r.Body, marker) {
			return true
		}
	}
	return false
}

func isNotFound(err error) bool {
	var httpErr *sdkerrors.HTTPError
	return errors.As(err, &httpErr) && httpErr.Code == http.StatusNotFound
}

// parseScore parses JavDB's score text, e.g. "4.32分, 由195人評價".
func parseScore(s string) float64 {
	if ss := scoreRegexp.FindStringSubmatch(s); len(ss) > 1 {
		v, _ := strconv.ParseFloat(ss[1], 64)
		return v
	}
	return 0
}

// normalizeNumber strips every separator from a movie number so that
// "IPVR-328", "ipvr328" and "IPVR_328" compare as equal.
func normalizeNumber(s string) string {
	return nonAlnumRegexp.ReplaceAllString(strings.ToUpper(s), "")
}
