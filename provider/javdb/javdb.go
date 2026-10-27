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
	"github.com/1103020472/metatube-sdk-go/provider/internal/actorname"
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

// digitRegexp and letterRegexp are used to compare movie numbers regardless of
// the label prefixes JavDB adds or drops.
var (
	digitRegexp  = regexp.MustCompile(`\d+`)
	letterRegexp = regexp.MustCompile(`[A-Za-z]`)
)

// fullNumberRegexp matches a complete movie number such as "MDVR-411",
// "SSIS001", "259LUXU-1234" or "FC2-PPV-1234567". Titles contain spaces or
// non-ASCII characters, so they never match and are therefore never filtered.
var fullNumberRegexp = regexp.MustCompile(`(?i)^[a-z\d]+(?:[-_][a-z\d]+)*\d$`)

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
	"JavDB 该条目需要登录后才能访问（常见于 FC2 等素人番号），请在服务端配置 JavDB cookie",
)

// errCookieExpired is returned when a session cookie was configured but no
// longer works (JavDB redirected a cookie-carrying request to the login
// page). It tells the user to refresh the cookie instead of setting one.
var errCookieExpired = sdkerrors.New(
	http.StatusUnauthorized,
	"JavDB cookie 已失效，请重新从浏览器复制 _jdb_session cookie 并更新配置",
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
// info by searching JavDB and picking the matching entry, if any.
func (javdb *JavDB) GetMovieInfoByNumber(number string) (*model.MovieInfo, error) {
	keyword := javdb.NormalizeMovieKeyword(number)
	if keyword == "" {
		return nil, provider.ErrInvalidID
	}

	results, err := javdb.searchMovie(keyword)
	if err != nil {
		return nil, err
	}

	// An identical number wins, JavDB's own rewrites are the second choice:
	// "259LUXU-1234" is listed as "LUXU-1234" and "FC2-PPV-1234567" as
	// "FC2-1234567". Trying exact matches first also keeps numbers such as
	// "MDVR-011" from matching "MDVR-11".
	if result := findResultByNumber(results, keyword, true); result != nil {
		return javdb.GetMovieInfoByURL(result.Homepage)
	}
	if result := findResultByNumber(results, keyword, false); result != nil {
		return javdb.GetMovieInfoByURL(result.Homepage)
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
		reqErr = javdb.responseError(r)
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

	// Actors, JavDB shows a placeholder such as "N/A" when the movie has no
	// credited actress, which must not end up as an actor name.
	c.OnXML(`//nav[@class="panel movie-panel-info"]//a[starts-with(@href, "/actors/")]`, func(e *colly.XMLElement) {
		if actor := strings.TrimSpace(e.Text); actor != "" && !actorname.IsPlaceholder(actor) {
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

// SearchMovie searches JavDB and drops the results that do not belong to the
// queried number. JavDB's own search is fuzzy: "MDVR-411" also returns
// MDVR-441, MDVR-421, MDVR-419 and so on, and only ranks the queried movie
// first. Since callers (e.g. Jellyfin's identify dialog) list every result,
// those unrelated entries are filtered out here.
func (javdb *JavDB) SearchMovie(keyword string) (results []*model.MovieSearchResult, err error) {
	keyword = javdb.NormalizeMovieKeyword(keyword)
	if keyword == "" {
		return nil, provider.ErrInvalidKeyword
	}

	results, err = javdb.searchMovie(keyword)
	if err != nil || len(results) == 0 {
		return results, err
	}

	if filtered := filterResultsByNumber(results, keyword); len(filtered) > 0 {
		return filtered, nil
	}
	// Nothing similar enough was found, keep JavDB's own results as a
	// best-effort fallback so that the caller can still pick manually.
	return results, nil
}

// searchMovie returns JavDB's raw search results, without any filtering.
func (javdb *JavDB) searchMovie(keyword string) (results []*model.MovieSearchResult, err error) {
	c := javdb.collector()

	var reqErr error
	c.OnResponse(func(r *colly.Response) {
		reqErr = javdb.responseError(r)
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
func (javdb *JavDB) responseError(r *colly.Response) error {
	switch {
	case r == nil:
		return nil
	case isCloudflareChallenge(r):
		return errCloudflare
	case isLoginRedirect(r):
		// A configured cookie that still lands on the login page has expired.
		if cookie, _ := javdb.cookie.Load().(string); cookie != "" {
			return errCookieExpired
		}
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

// trailingDigits returns the last group of digits of a movie number, so that
// "LUXU-1234" and "259LUXU-1234" both yield "1234". Leading zeros are ignored.
func trailingDigits(s string) string {
	runs := digitRegexp.FindAllString(s, -1)
	if len(runs) == 0 {
		return ""
	}
	if digits := strings.TrimLeft(runs[len(runs)-1], "0"); digits != "" {
		return digits
	}
	return "0"
}

// lettersOnly returns the letters of a movie number in upper case, so that
// "FC2-PPV-1234567" yields "FCPPV".
func lettersOnly(s string) string {
	return strings.Join(letterRegexp.FindAllString(strings.ToUpper(s), -1), "")
}

// sameNumber reports whether two movie numbers refer to the same movie even
// though JavDB spells them differently. JavDB drops some label prefixes, e.g.
// "259LUXU-1234" is listed as "LUXU-1234" and "FC2-PPV-1234567" as
// "FC2-1234567", so the letters only need to be compatible while the trailing
// digits have to match exactly.
func sameNumber(a, b string) bool {
	digits := trailingDigits(a)
	if digits == "" || digits != trailingDigits(b) {
		return false
	}
	aLetters, bLetters := lettersOnly(a), lettersOnly(b)
	return strings.HasPrefix(aLetters, bLetters) || strings.HasSuffix(aLetters, bLetters) ||
		strings.HasPrefix(bLetters, aLetters) || strings.HasSuffix(bLetters, aLetters)
}

// matchesNumber reports whether a number refers to the wanted one. When exact
// is set, the numbers have to be identical; otherwise JavDB's rewrites of the
// label prefix are tolerated as well.
func matchesNumber(number, wanted string, exact bool) bool {
	if normalizeNumber(number) == normalizeNumber(wanted) {
		return true
	}
	return !exact && sameNumber(number, wanted)
}

// findResultByNumber returns the first result referring to the wanted number.
func findResultByNumber(results []*model.MovieSearchResult, wanted string, exact bool) *model.MovieSearchResult {
	for _, result := range results {
		if matchesNumber(result.Number, wanted, exact) {
			return result
		}
	}
	return nil
}

// filterResultsByNumber returns the results that refer to the wanted number.
// It returns nil for keywords that are not a complete movie number, and when
// no result is similar enough, so that the caller can fall back to the raw
// search results.
func filterResultsByNumber(results []*model.MovieSearchResult, wanted string) []*model.MovieSearchResult {
	if !fullNumberRegexp.MatchString(wanted) {
		return nil
	}
	// Identical numbers win over JavDB's rewritten ones.
	if exact := filterResults(results, wanted, true); len(exact) > 0 {
		return exact
	}
	return filterResults(results, wanted, false)
}

func filterResults(results []*model.MovieSearchResult, wanted string, exact bool) (filtered []*model.MovieSearchResult) {
	for _, result := range results {
		if matchesNumber(result.Number, wanted, exact) {
			filtered = append(filtered, result)
		}
	}
	return filtered
}
