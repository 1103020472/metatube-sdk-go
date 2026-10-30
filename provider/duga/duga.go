package duga

import (
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gocolly/colly/v2"
	"golang.org/x/text/language"

	"github.com/1103020472/metatube-sdk-go/common/number"
	"github.com/1103020472/metatube-sdk-go/common/parser"
	"github.com/1103020472/metatube-sdk-go/model"
	"github.com/1103020472/metatube-sdk-go/provider"
	"github.com/1103020472/metatube-sdk-go/provider/internal/scraper"
)

var (
	_ provider.MovieProvider = (*DUGA)(nil)
	_ provider.MovieSearcher = (*DUGA)(nil)
	_ provider.MovieReviewer = (*DUGA)(nil)
)

const (
	Name     = "DUGA"
	Priority = 1000 - 2
)

const (
	baseURL   = "https://duga.jp/"
	movieURL  = "https://duga.jp/ppv/%s/"
	searchURL = "https://duga.jp/search/=/q=%s/"

	// reviewPageURL is the "show all reviews" page of a product. The detail
	// page only renders the newest review, the complete list lives here and
	// needs no login.
	//
	// `viewtype=1` is the pay-per-view variant, i.e. the one addressed by
	// movieURL; it is also what DUGA falls back to when the parameter is
	// omitted, so passing it explicitly only pins down the behaviour.
	reviewPageURL = "https://duga.jp/prog/showreview/?productid=%s&viewtype=1&page=%d"
)

const (
	// reviewPageSize is the number of reviews DUGA renders per page.
	reviewPageSize = 10

	// maxReviewPages bounds the pagination loop. DUGA reviews are sparse --
	// the most reviewed titles sampled so far stay below 20 -- so this is
	// only a guard against a runaway loop, not a real limit.
	maxReviewPages = 10
)

type DUGA struct {
	*scraper.Scraper
}

func New() *DUGA {
	return &DUGA{
		Scraper: scraper.NewDefaultScraper(Name, baseURL, Priority, language.Japanese),
	}
}

func (duga *DUGA) NormalizeMovieID(id string) string {
	return strings.ToLower(id) // DUGA always use lowercase id.
}

func (duga *DUGA) GetMovieInfoByID(id string) (info *model.MovieInfo, err error) {
	return duga.GetMovieInfoByURL(fmt.Sprintf(movieURL, id))
}

func (duga *DUGA) ParseMovieIDFromURL(rawURL string) (string, error) {
	homepage, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	return duga.NormalizeMovieID(path.Base(homepage.Path)), nil
}

func (duga *DUGA) GetMovieInfoByURL(rawURL string) (info *model.MovieInfo, err error) {
	id, err := duga.ParseMovieIDFromURL(rawURL)
	if err != nil {
		return
	}

	info = &model.MovieInfo{
		ID:            id,
		Provider:      duga.Name(),
		Homepage:      rawURL,
		Actors:        []string{},
		PreviewImages: []string{},
		Genres:        []string{},
	}

	c := duga.ClonedCollector()

	// Title
	c.OnXML(`//*[@id="contentsname"]`, func(e *colly.XMLElement) {
		info.Title = e.Text
	})

	// Summary+JSON
	c.OnXML(`//script[@type="application/ld+json"]`, func(e *colly.XMLElement) {
		data := struct {
			// Name        string `json:"name"`
			Description string `json:"description"`
		}{}
		if json.NewDecoder(strings.NewReader(e.Text)).Decode(&data) == nil {
			info.Summary = data.Description
		}
	})

	// Thumb
	c.OnXML(`//div[@class="imagebox"]//img[@id="productjpg"]`, func(e *colly.XMLElement) {
		info.ThumbURL = e.Request.AbsoluteURL(e.Attr("src"))
	})

	// Cover
	c.OnXML(`//div[@class="imagebox"]/a`, func(e *colly.XMLElement) {
		info.CoverURL = e.Request.AbsoluteURL(e.Attr("href"))
	})

	// Fields
	c.OnXML(`//div[@class="summaryinner"]//table//tr`, func(e *colly.XMLElement) {
		switch e.ChildText(`.//th`) {
		case "配信開始日", "発売日":
			if time.Time(info.ReleaseDate).IsZero() {
				info.ReleaseDate = parser.ParseDate(e.ChildText(`.//td`))
			}
		case "メーカー":
			info.Maker = strings.TrimSpace(e.ChildText(`.//td`))
		case "レーベル":
			info.Label = strings.TrimSpace(e.ChildText(`.//td`))
		case "作品ID":
			info.ID = strings.TrimSpace(e.ChildText(`.//td`))
		case "メーカー品番":
			info.Number = strings.TrimSpace(e.ChildText(`.//td`))
		case "シリーズ":
			info.Series = strings.TrimSpace(e.ChildText(`.//td`))
		case "出演者", "監督", "カテゴリ":
			// parse later.
		}
	})

	// Director
	c.OnXML(`//ul[@class="director"]//li//a`, func(e *colly.XMLElement) {
		if info.Director != "" {
			return // ignore others.
		}
		info.Director = e.Text
	})

	// Actors
	c.OnXML(`//ul[@class="performer"]//li//a`, func(e *colly.XMLElement) {
		info.Actors = append(info.Actors, e.Text)
	})

	// Genres
	c.OnXML(`//ul[@class="categorylist"]//li//a`, func(e *colly.XMLElement) {
		info.Genres = append(info.Genres, e.Text)
	})

	// Title (fallback)
	c.OnXML(`//meta[@property="og:title"]`, func(e *colly.XMLElement) {
		if info.Title != "" {
			return
		}
		info.Title = e.Attr("content")
	})

	// Summary (fallback)
	c.OnXML(`//meta[@property="og:description"]`, func(e *colly.XMLElement) {
		if info.Summary != "" {
			return
		}
		info.Summary = e.Attr("content")
	})

	// Thumb (fallback)
	c.OnXML(`//meta[@property="og:image"]`, func(e *colly.XMLElement) {
		if info.ThumbURL != "" {
			return
		}
		info.ThumbURL = e.Request.AbsoluteURL(e.Attr("content"))
	})

	// Score
	c.OnXML(`//div[@class="summaryinner"]//div[@class="ratingstar-total"]`, func(e *colly.XMLElement) {
		info.Score = parser.ParseScore(e.ChildAttr(`.//img`, "alt"))
	})

	// Runtime
	c.OnXML(`//div[@class="downloadbox"]//table//tr`, func(e *colly.XMLElement) {
		if info.Runtime > 0 {
			return
		}
		if e.ChildText(`.//th`) == "再生時間" {
			info.Runtime = parser.ParseRuntime(e.ChildText(`.//td`))
		}
	})

	// Preview Video
	c.OnXML(`//video[@class="play-video"]`, func(e *colly.XMLElement) {
		info.PreviewVideoURL = e.Request.AbsoluteURL(e.Attr("src"))
	})

	// Preview Images
	c.OnXML(`//*[@id="digestthumbbox"]/li`, func(e *colly.XMLElement) {
		info.PreviewImages = append(info.PreviewImages,
			e.Request.AbsoluteURL(e.ChildAttr(`.//a`, "href")))
	})

	// Multiple (fallback)
	c.OnScraped(func(_ *colly.Response) {
		if info.CoverURL == "" {
			// use thumb as cover.
			info.CoverURL = info.ThumbURL
		}
		if info.ID == "" {
			// fallback to id again.
			info.ID = id
		}
		if info.Number == "" {
			// use ID as number.
			info.Number = strings.ToUpper(info.ID)
		}
	})

	err = c.Visit(info.Homepage)
	return
}

// GetMovieReviewsByID gets the user reviews of the given movie id.
func (duga *DUGA) GetMovieReviewsByID(id string) ([]*model.MovieReviewDetail, error) {
	return duga.getMovieReviews(id)
}

// GetMovieReviewsByURL gets the user reviews of the given movie URL.
func (duga *DUGA) GetMovieReviewsByURL(rawURL string) ([]*model.MovieReviewDetail, error) {
	id, err := duga.ParseMovieIDFromURL(rawURL)
	if err != nil {
		return nil, err
	}
	return duga.getMovieReviews(id)
}

func (duga *DUGA) getMovieReviews(id string) (reviews []*model.MovieReviewDetail, err error) {
	if id = duga.NormalizeMovieID(strings.TrimSpace(id)); id == "" {
		return nil, nil
	}

	var total int
	c := duga.ClonedCollector()

	// `paging-sum` is only rendered when the product actually has reviews,
	// and it is the only reliable source for the total count: requesting an
	// out-of-range page does not fail, DUGA simply renders the last page
	// again, so "an empty page means we are done" does not hold here.
	c.OnXML(`//div[@class="paging-sum"]`, func(e *colly.XMLElement) {
		if ss := reviewTotalRegexp.FindStringSubmatch(e.Text); len(ss) == 2 {
			total, _ = strconv.Atoi(ss[1])
		}
	})

	c.OnXML(`//div[@class="userreview"]//div[@class="eachreview"]`, func(e *colly.XMLElement) {
		author := strings.TrimSpace(e.ChildText(`.//div[@class="reviewer"]/a`))
		if author == "" {
			// Reviews written by deleted accounts render the name as
			// plain text instead of a link.
			author = strings.TrimSpace(strings.TrimPrefix(
				strings.TrimSpace(e.ChildText(`.//div[@class="reviewer"]`)), "レビュアー："))
		}
		comment := strings.TrimSpace(e.ChildText(`.//p[@class="comment"]`))
		if author == "" || comment == "" {
			return
		}

		reviews = append(reviews, &model.MovieReviewDetail{
			Title:   strings.TrimSpace(e.ChildText(`.//div[@class="title"]`)),
			Author:  author,
			Comment: comment,
			Score:   parseReviewScore(e),
			// The span text is the plain date (`2022年06月12日`), its title
			// attribute carries the time as well but no parser handles that
			// format.
			Date: parser.ParseDate(e.ChildText(`.//div[@class="date"]/span`)),
		})
	})

	if err = c.Visit(fmt.Sprintf(reviewPageURL, id, 1)); err != nil {
		return nil, err
	}

	pages := (total + reviewPageSize - 1) / reviewPageSize
	if pages > maxReviewPages {
		pages = maxReviewPages
	}
	for page := 2; page <= pages; page++ {
		if err = c.Visit(fmt.Sprintf(reviewPageURL, id, page)); err != nil {
			return nil, err
		}
	}
	return reviews, nil
}

// reviewTotalRegexp extracts the total review count from DUGA's pager, whose
// text reads `1～10件 / 全15件`.
var reviewTotalRegexp = regexp.MustCompile(`全\s*(\d+)\s*件`)

// reviewScoreRegexp extracts the rating from DUGA's star image, whose URL ends
// with e.g. `score/5.0.gif`.
var reviewScoreRegexp = regexp.MustCompile(`score/(\d+(?:\.\d+)?)\.gif`)

// parseReviewScore reads the 0-5 rating of a single review. The "show all
// reviews" page only encodes it in the star image URL, while the detail page
// also sets an alt attribute.
func parseReviewScore(e *colly.XMLElement) float64 {
	const img = `.//div[@class="ratingstar"]/img`

	if alt := strings.TrimSpace(e.ChildAttr(img, "alt")); alt != "" {
		if score, err := strconv.ParseFloat(alt, 64); err == nil {
			return score
		}
	}
	if ss := reviewScoreRegexp.FindStringSubmatch(e.ChildAttr(img, "src")); len(ss) == 2 {
		if score, err := strconv.ParseFloat(ss[1], 64); err == nil {
			return score
		}
	}
	return 0
}

func (duga *DUGA) NormalizeMovieKeyword(keyword string) string {
	keyword = strings.TrimSpace(keyword)
	if number.IsSpecial(keyword) {
		return ""
	}
	return strings.ToUpper(keyword)
}

// SearchMovie searches DUGA and drops the entries that do not refer to the
// queried number.
//
// DUGA's search page is not a reliable source of matches: when nothing
// matches, it still renders a "あなたへのおすすめ作品" (recommended for you)
// carousel on the very same `#searchresultarea` area, using the exact same
// `div.contentslist` markup as real search hits. The carousel is only
// rendered for sessions that already carry DUGA's cookies (i.e. after any
// detail page has been visited), which is always the case here because the
// provider's collector is a long-lived singleton -- so a plain "take the
// first N items" approach ends up returning 3 completely unrelated movies
// for every unknown number.
func (duga *DUGA) SearchMovie(keyword string) (results []*model.MovieSearchResult, err error) {
	c := duga.ClonedCollector()

	var (
		// noResults is set when DUGA explicitly reports that nothing
		// matched the keyword.
		noResults bool
		ids       []string
		seen      = make(map[string]struct{})
	)

	c.OnXML(`//*[@id="searchresultarea"]//div[@class="nosearch-msg"]`, func(_ *colly.XMLElement) {
		noResults = true
	})

	// The "recommended for you" carousel lives in a `liquidcontentslist`
	// wrapper, exclude it so that only real search hits are collected.
	c.OnXML(`//*[@id="searchresultarea"]//div[@class="contentslist"][not(ancestor::div[contains(@class, "liquidcontentslist")])]`, func(e *colly.XMLElement) {
		id, idErr := duga.ParseMovieIDFromURL(e.Request.AbsoluteURL(e.ChildAttr(`.//a`, "href")))
		if idErr != nil || id == "" {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	})

	if err = c.Visit(fmt.Sprintf(searchURL, url.QueryEscape(keyword))); err != nil {
		return nil, err
	}
	if noResults || len(ids) == 0 {
		return nil, nil
	}

	const limit = 3
	if len(ids) > limit {
		ids = ids[:limit]
	}

	// Fetch details concurrently but keep the search result order, so that
	// the returned slice is deterministic.
	fetched := make([]*model.MovieSearchResult, len(ids))
	var wg sync.WaitGroup
	for i := range ids {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if info, _ := duga.GetMovieInfoByID(ids[i]); info != nil && info.IsValid() {
				fetched[i] = info.ToSearchResult()
			}
		}(i)
	}
	wg.Wait()

	for _, result := range fetched {
		if result == nil {
			continue
		}
		if !matchesNumber(result, keyword) {
			continue
		}
		results = append(results, result)
	}
	return results, nil
}

// fullNumberRegexp matches a complete movie number such as "MIZD-398",
// "31ID-036" or "tma2-0526".
var fullNumberRegexp = regexp.MustCompile(`(?i)^[a-z\d]+(?:[-_][a-z\d]+)*\d$`)

// matchesNumber reports whether the search result refers to the queried
// keyword. Non-number keywords (titles, actors, ...) are never filtered, since
// there is nothing to compare against.
func matchesNumber(result *model.MovieSearchResult, keyword string) bool {
	if !fullNumberRegexp.MatchString(keyword) {
		return true
	}
	wanted := canonicalNumber(keyword)
	return canonicalNumber(result.Number) == wanted ||
		canonicalNumber(result.ID) == wanted
}

// canonicalNumber normalizes a movie number for comparison: lowercase, drop
// every separator and strip the leading zeros of each digit run, so that
// "DSVR-030", "DSVR-0030" and "dsvr30" are all considered equal.
func canonicalNumber(s string) string {
	var b strings.Builder
	var inDigit, leading bool
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z':
			inDigit = false
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			if !inDigit {
				inDigit, leading = true, true
			}
			if leading && r == '0' {
				continue
			}
			leading = false
			b.WriteRune(r)
		default:
			inDigit = false
		}
	}
	return b.String()
}

func init() {
	provider.Register(Name, New)
}
