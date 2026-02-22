package avleague

import (
	"fmt"
	"log"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/gocolly/colly/v2"
	"golang.org/x/text/language"
	dt "gorm.io/datatypes"

	"github.com/1103020472/metatube-sdk-go/common/parser"
	"github.com/1103020472/metatube-sdk-go/model"
	"github.com/1103020472/metatube-sdk-go/provider"
	"github.com/1103020472/metatube-sdk-go/provider/internal/scraper"
)

var (
	_ provider.ActorProvider = (*AVLeague)(nil)
	_ provider.ActorSearcher = (*AVLeague)(nil)
)

const (
	Name     = "AV-LEAGUE" // `AV.LEAGUE`
	Priority = 1000
)

const (
	baseURL   = "https://www.av-league.com/"
	actorURL  = "https://www.av-league.com/actress/%s.html"
	searchURL = "https://www.av-league.com/search/search.php?k=%s"
)

type AVLeague struct {
	*scraper.Scraper
}

func New() *AVLeague {
	return &AVLeague{scraper.NewDefaultScraper(
		Name, baseURL, Priority,
		language.Japanese,
		scraper.WithDisableCookies(),
	)}
}

func (avl *AVLeague) GetActorInfoByID(id string) (info *model.ActorInfo, err error) {
	return avl.GetActorInfoByURL(fmt.Sprintf(actorURL, id))
}

func (avl *AVLeague) ParseActorIDFromURL(rawURL string) (id string, err error) {
	homepage, err := url.Parse(rawURL)
	if err != nil {
		return
	}
	if ext := path.Ext(homepage.Path); ext != "" {
		id = path.Base(homepage.Path[:len(homepage.Path)-len(ext)])
	}
	return
}

func (avl *AVLeague) GetActorInfoByURL(rawURL string) (info *model.ActorInfo, err error) {
	id, err := avl.ParseActorIDFromURL(rawURL)
	if err != nil {
		return
	}

	info = &model.ActorInfo{
		ID:       id,
		Provider: avl.Name(),
		Homepage: rawURL,
		Aliases:  []string{},
		Images:   []string{},
	}

	c := avl.ClonedCollector()

	// Name
	c.OnXML(`//*[@id="pan"]/span`, func(e *colly.XMLElement) {
		info.Name = strings.TrimSpace(e.Text)
	})

	// Aliases
	c.OnXML(`//*[@id="j-prof"]/span`, func(e *colly.XMLElement) {
		if name, aliases, found := strings.Cut(e.Text, ":"); found {
			if !strings.Contains(name, "別名") {
				return // may not be an alias
			}
			for _, alias := range strings.Split(aliases, "、") {
				info.Aliases = append(info.Aliases, strings.TrimSpace(alias))
			}
		}
	})

	// Image (profile)
	c.OnXML(`//*[@id="contents"]/div[@class="i-pic-box"]/div/img`, func(e *colly.XMLElement) {
		info.Images = append(info.Images, e.Request.AbsoluteURL(e.Attr("src")))
	})

	// Image (fallback)
	c.OnXML(`//meta[@property="og:image"]`, func(e *colly.XMLElement) {
		if len(info.Images) == 0 {
			info.Images = append(info.Images, e.Request.AbsoluteURL(e.Attr("content")))
		}
	})

	// Fields
	c.OnXML(`//*[@id="contents"]//table/tbody/tr`, func(e *colly.XMLElement) {
		row := strings.TrimSpace(e.ChildText(`.//th`))
		data := strings.TrimSpace(e.ChildText(`.//td`))
		if data == "不明" || data == "なし" {
			return // ignore unknown
		}
		// lj-增加推特和ins的超链接，以及演员标签
		switch row {
		case "3サイズ":
			B, W, H, Cup := parseMeasurements(data)
			// lj-任何一个不为0，都能转换
			if B != 0 || W != 0 || H != 0 {
				f := func(v int) interface{} {
					if v == 0 {
						return "-"
					}
					return v
				}
				info.Measurements = fmt.Sprintf("B:%v / W:%v / H:%v", f(B), f(W), f(H))
			}
			info.CupSize = Cup
		case "身長":
			info.Height = parser.ParseInt(strings.TrimRight(data, "cm"))
		case "血液型":
			info.BloodType = strings.TrimSpace(strings.TrimRight(data, "型"))
		case "生年月日":
			info.Birthday = parseDate(data)
		case "出身":
			info.Nationality = data
		case "デビュー":
			info.DebutDate = parseDate(data)
		case "Twitter":
			info.Twitter = parseHrefV2(data)
		case "インスタ":
			info.Instagram = parseHref(data)
		case "タグ":
			info.Tags = parseTag(data)
		}
	})

	err = c.Visit(info.Homepage)
	return
}

func (avl *AVLeague) SearchActor(keyword string) (results []*model.ActorSearchResult, err error) {
	c := avl.ClonedCollector()

	c.OnXML(`//*[@id="contents"]/div/div`, func(e *colly.XMLElement) {
		homepage := e.Request.AbsoluteURL(
			e.ChildAttr(`.//div[@class="l-name"]/a`, "href"))
		id, _ := avl.ParseActorIDFromURL(homepage)
		// Name
		actor := strings.TrimSpace(e.ChildText(`.//div[@class="l-name"]/a`))
		// lj-还不清楚演员的多张图片是怎么来的，知道的话看下是否有优化空间
		// Images
		var images []string
		if img := e.ChildAttr(`.//div[@class="l-pic"]/a/img`, "data-layzr" /* lazy loading */); img != "" {
			images = []string{e.Request.AbsoluteURL(img)}
		}

		results = append(results, &model.ActorSearchResult{
			ID:       id,
			Name:     actor,
			Images:   images,
			Provider: avl.Name(),
			Homepage: homepage,
		})
	})

	err = c.Visit(fmt.Sprintf(searchURL, url.QueryEscape(keyword)))
	return
}

// lj-兼容没有BWH的场景，也能取到cup
func parseMeasurements(s string) (B, W, H int, Cup string) {
	for _, item := range strings.Split(s, "/") {
		name, data, found := strings.Cut(item, ":")
		if !found {
			continue
		}
		switch strings.TrimSpace(name) {
		case "B":
			// 改进版：将 \d+ 改为 [-\d]*，表示匹配数字或减号   兼容B: - （A） / W: - / H: -
			if ss := regexp.MustCompile(`([-\d]*)（([A-Z])）`).FindStringSubmatch(data); len(ss) == 3 {
				B = parser.ParseInt(ss[1])
				Cup = ss[2]
			}
		case "W":
			W = parser.ParseInt(data)
		case "H":
			H = parser.ParseInt(data)
		}
	}
	return
}

func parseDate(s string) (date dt.Date) {
	defer func() {
		if !time.Time(date).IsZero() {
			return
		}
		if ss := regexp.MustCompile(`([\s\d]+)年`).
			FindStringSubmatch(s); len(ss) == 2 {
			date = dt.Date(time.Date(parser.ParseInt(ss[1]),
				2, 2, 2, 2, 2, 2, time.UTC))
		}
	}()
	return parser.ParseDate(s)
}

func parseHrefV2(s string) (href string) {
	log.Print("test")
	return s
}

func parseHref(s string) (href string) {
	log.Print("--------------lj---------------parseHref-source:" + href)
	// 1. 去除两端可能存在的空格和双引号
	text := strings.TrimSpace(s)
	text = strings.Trim(s, "\"")
	text = strings.TrimSpace(text)

	// 2. 定义需要去除的特定前缀
	// 考虑到抓取时可能出现的多种变体，这里可以做容错处理
	prefixes := []string{"https:x.com", "https://x.com/"}

	for _, p := range prefixes {
		if strings.HasPrefix(text, p) {
			text = strings.TrimPrefix(text, p)
			break // 匹配到一个就跳出
		}
	}

	// 3. 如果处理完后还残留着斜杠（针对 https:x.com/username 情况）
	text = strings.TrimPrefix(text, "/")
	log.Print("--------------lj---------------parseHref:" + text)

	return text
}

func parseTag(s string) []string {
	// s 是由 strings.TrimSpace(e.ChildText(`.//td`)) 传入的字符串
	// 输出示例: "30代、巨乳、レズ..."

	// 1. 按中文/日文逗号分割
	rawTags := strings.Split(s, "、")

	var cleanTags []string
	for _, tag := range rawTags {
		t := strings.TrimSpace(tag)
		if t != "" {
			cleanTags = append(cleanTags, t)
		}
	}
	return cleanTags
}

func init() {
	provider.Register(Name, New)
}
