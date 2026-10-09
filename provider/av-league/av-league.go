package avleague

import (
	"fmt"
	"log"
	"net/url"
	"path"
	"regexp"
	"strconv"
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

// av-wiki 相关：av-league 与 av-wiki 的演员资料会合并返回（av-league 优先）。
const (
	wikiActorURL  = "https://av-wiki.net/av-actress/%s/"
	wikiSearchURL = "https://av-wiki.net/?s=%s&post_type=product"
)

// av-league 演员 ID 为纯数字；av-wiki 演员 ID 为小写字母/数字/连字符组成的 slug。
var (
	numericActorIDRe = regexp.MustCompile(`^\d+$`)
	wikiActorIDRe    = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
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

// ExtraHosts 声明 av-league 还会从 av-wiki.net 兜底提供演员数据，
// 使 engine 能把 av-wiki 的 URL 路由回本 provider。
func (avl *AVLeague) ExtraHosts() []string {
	return []string{"av-wiki.net"}
}

func (avl *AVLeague) GetActorInfoByID(id string) (info *model.ActorInfo, err error) {
	if isWikiActorID(id) {
		// av-league 无此演员、降级使用 av-wiki 的 ID，返回 av-wiki 的 URL。
		return avl.GetActorInfoByURL(fmt.Sprintf(wikiActorURL, id))
	}
	return avl.GetActorInfoByURL(fmt.Sprintf(actorURL, id))
}

// isWikiActorID 判断 ID 是否为 av-wiki 的 slug（非纯数字且符合 slug 规则）。
func isWikiActorID(id string) bool {
	id = strings.ToLower(strings.TrimSpace(id))
	return !numericActorIDRe.MatchString(id) && wikiActorIDRe.MatchString(id)
}

// isWikiActorURL 判断 URL 是否为 av-wiki 的演员详情页。
func isWikiActorURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(u.Host), "av-wiki.net") &&
		strings.Contains(u.Path, "/av-actress/")
}

func (avl *AVLeague) ParseActorIDFromURL(rawURL string) (id string, err error) {
	homepage, err := url.Parse(rawURL)
	if err != nil {
		return
	}

	// av-wiki 演员详情页：/av-actress/<slug>/（可能带分页 /page/2）。
	if strings.Contains(homepage.Path, "/av-actress/") {
		parts := strings.Split(strings.Trim(homepage.Path, "/"), "/")
		for i, p := range parts {
			if p == "av-actress" && i+1 < len(parts) {
				return parts[i+1], nil
			}
		}
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

	// av-wiki 演员详情页：直接按 wiki 结构解析。
	if isWikiActorURL(rawURL) {
		return avl.parseWikiActorDetail(rawURL)
	}

	info, err = avl.parseAvLeagueActorDetail(rawURL, id)
	if err != nil {
		return
	}

	// av-wiki 合并（av-league 优先）：av-league 的演员页没有简介，
	// 所以不管 av-league 的字段是否齐全，都要查一次 av-wiki 再合并。
	if name := strings.TrimSpace(info.Name); name != "" {
		if wikiInfo, wikiErr := avl.fetchActorInfoFromWiki(name); wikiErr == nil && wikiInfo != nil {
			mergeActorInfoFromWiki(info, wikiInfo)
		} else if wikiErr != nil {
			log.Printf("av-wiki 查询演员失败 %s: %v", name, wikiErr)
		}
	}
	return
}

// parseAvLeagueActorDetail 解析 av-league 演员详情页。
func (avl *AVLeague) parseAvLeagueActorDetail(rawURL, id string) (info *model.ActorInfo, err error) {
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
			info.Twitter = parseSNSUsername(e, data, "twitter.com", "x.com")
		case "インスタ":
			info.Instagram = parseSNSUsername(e, data, "instagram.com")
		case "タグ":
			info.Tags = parseTag(data)
		}
	})

	err = c.Visit(info.Homepage)
	return
}

func (avl *AVLeague) SearchActor(keyword string) (results []*model.ActorSearchResult, err error) {
	// 优先从 av-league 搜索。
	results, err = avl.searchAvLeagueActor(keyword)
	if len(results) > 0 {
		return results, nil
	}

	// av-league 完全搜不到，降级从 av-wiki 搜索（ID/URL 使用 av-wiki 的）。
	wikiResults, wikiErr := avl.searchWikiActor(keyword)
	if wikiErr == nil && len(wikiResults) > 0 {
		log.Printf("av-league 未搜到，通过 av-wiki 搜索演员：%s", keyword)
		return wikiResults, nil
	}
	return results, err
}

// searchAvLeagueActor 从 av-league 搜索演员。
func (avl *AVLeague) searchAvLeagueActor(keyword string) (results []*model.ActorSearchResult, err error) {
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

// searchWikiActor 从 av-wiki 搜索演员（av-league 搜不到时的来源）。
// 返回以 av-wiki slug 为 ID、av-wiki 地址为 Homepage 的结果，
// 头像取自演员详情页（av-wiki 的搜索结果列表只有作品图，没有演员头像）。
func (avl *AVLeague) searchWikiActor(keyword string) (results []*model.ActorSearchResult, err error) {
	info, err := avl.fetchActorInfoFromWiki(keyword)
	if err != nil {
		return nil, err
	}
	return []*model.ActorSearchResult{{
		ID:       info.ID,
		Name:     info.Name,
		Provider: avl.Name(),
		Homepage: info.Homepage,
		Images:   info.Images,
	}}, nil
}

// ===================== av-wiki 演员资料 =====================

// wikiActorCandidate 是搜索结果 / 作品页中解析出的演员候选（名字 + 详情页地址）。
type wikiActorCandidate struct {
	name     string
	homepage string
}

// collectWikiSearchCandidates 按关键字搜索 av-wiki，返回搜索结果中的演员候选
// （名字 + 详情页地址）与作品页地址列表。
func (avl *AVLeague) collectWikiSearchCandidates(keyword string) (candidates []wikiActorCandidate, productURLs []string, err error) {
	c := avl.ClonedCollector()
	c.SetRequestTimeout(30 * time.Second)
	// 搜索结果每个作品条目里的演员名链接，直接指向演员详情页。
	c.OnXML(`//article[contains(@class,"archive-list")]//li[@class="actress-name"]/a`, func(e *colly.XMLElement) {
		href := e.Request.AbsoluteURL(e.Attr("href"))
		text := strings.TrimSpace(e.Text)
		if href != "" && text != "" {
			candidates = append(candidates, wikiActorCandidate{name: text, homepage: href})
		}
	})
	// 作品页地址（搜索结果未直接给出演员链接时，进入作品页再匹配）。
	c.OnXML(`//article[contains(@class,"archive-list")]//div[@class="read-more"]/a`, func(e *colly.XMLElement) {
		productURLs = append(productURLs, e.Request.AbsoluteURL(e.Attr("href")))
	})
	err = c.Visit(fmt.Sprintf(wikiSearchURL, url.QueryEscape(keyword)))
	return
}

// fetchActorInfoFromWiki 从 av-wiki.net 获取演员资料，流程：
//  1. 按演员名搜索，搜索结果是该演员相关的番号(作品)列表；
//  2. 通过作品的演员列表定位演员详情页：先按主名精确匹配；主名不一致时（如 av-league
//     用名恰好是 av-wiki 的别名），再用精确方式匹配 av-wiki 明确登记的别名；
//  3. 访问演员详情页，解析简介、身高、出生日期、三围、别名、SNS、头像等数据。
//
// 全程为精确匹配（不做相似度模糊匹配），避免误关联到无关演员。
func (avl *AVLeague) fetchActorInfoFromWiki(keyword string) (info *model.ActorInfo, err error) {
	if keyword = strings.TrimSpace(keyword); keyword == "" {
		return nil, provider.ErrInvalidKeyword
	}

	// 第一步：搜索演员，收集候选的演员详情页链接与作品页链接。
	candidates, productURLs, err := avl.collectWikiSearchCandidates(keyword)
	if err != nil {
		return nil, err
	}
	uniq := dedupWikiCandidates(candidates) // 保留 wiki 搜索相关度顺序。

	// 第二步（快路径）：主名精确匹配，只解析命中的详情页。
	for _, c := range uniq {
		if wikiNamesEqual(c.name, keyword) {
			return avl.parseWikiActorDetail(c.homepage)
		}
	}

	// 第二步（别名路径）：主名未直接命中时，解析前若干候选详情页，
	// 用 keyword 精确匹配其主名或明确登记的别名。
	if info, ok := avl.parseAndMatchWikiCandidates(keyword, uniq, 3); ok {
		return info, nil
	}

	// 最后：搜索结果未直接给出演员链接时，进入作品页收集演员再匹配。
	for i, productURL := range productURLs {
		if i >= 3 { // 限制额外请求数量，避免过多访问。
			break
		}
		productCandidates := avl.collectCandidatesFromProduct(productURL)
		if info, ok := avl.parseAndMatchWikiCandidates(keyword, productCandidates, 1); ok {
			return info, nil
		}
	}
	return nil, provider.ErrInfoNotFound
}

// collectCandidatesFromProduct 解析单个 av-wiki 作品页，返回其演员列表候选。
// 作品元数据 dl.dltable 中，仅「AV女優名」对应的 dd 内有链接。
func (avl *AVLeague) collectCandidatesFromProduct(productURL string) (candidates []wikiActorCandidate) {
	c := avl.ClonedCollector()
	c.SetRequestTimeout(30 * time.Second)
	c.OnXML(`//dl[contains(@class,"dltable")]//dd//a`, func(e *colly.XMLElement) {
		href := e.Request.AbsoluteURL(e.Attr("href"))
		text := strings.TrimSpace(e.Text)
		if href != "" && text != "" {
			candidates = append(candidates, wikiActorCandidate{name: text, homepage: href})
		}
	})
	_ = c.Visit(productURL)
	return
}

// parseAndMatchWikiCandidates 依次解析候选详情页（最多 limit 个），
// 返回第一个主名或别名与 keyword 精确匹配的演员信息。
func (avl *AVLeague) parseAndMatchWikiCandidates(keyword string, candidates []wikiActorCandidate, limit int) (*model.ActorInfo, bool) {
	for i, c := range dedupWikiCandidates(candidates) {
		if i >= limit {
			break
		}
		info, err := avl.parseWikiActorDetail(c.homepage)
		if err != nil {
			continue
		}
		if actorInfoMatchesKeyword(info, keyword) {
			return info, true
		}
	}
	return nil, false
}

// actorInfoMatchesKeyword 判断 keyword 是否与演员的主名或任一别名精确（规范化后）相等。
func actorInfoMatchesKeyword(info *model.ActorInfo, keyword string) bool {
	if wikiNamesEqual(info.Name, keyword) {
		return true
	}
	for _, alias := range info.Aliases {
		if wikiNamesEqual(alias, keyword) {
			return true
		}
	}
	return false
}

// wikiNamesEqual 判断两个名字规范化后是否完全一致。
func wikiNamesEqual(a, b string) bool {
	return normalizeWikiActorName(a) == normalizeWikiActorName(b)
}

// dedupWikiCandidates 按详情页地址去重，保留首次出现顺序。
func dedupWikiCandidates(candidates []wikiActorCandidate) []wikiActorCandidate {
	var uniq []wikiActorCandidate
	seen := make(map[string]struct{})
	for _, c := range candidates {
		if _, ok := seen[c.homepage]; ok {
			continue
		}
		seen[c.homepage] = struct{}{}
		uniq = append(uniq, c)
	}
	return uniq
}

// normalizeWikiActorName 规范化演员名用于匹配：转小写并去除所有空白。
func normalizeWikiActorName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	return strings.NewReplacer(" ", "", "　", "", "\t", "", "\n", "", "\r", "").Replace(s)
}

// parseWikiActorDetail 解析 av-wiki 演员详情页。
func (avl *AVLeague) parseWikiActorDetail(detailURL string) (info *model.ActorInfo, err error) {
	id, err := parseWikiActorID(detailURL)
	if err != nil {
		return nil, err
	}
	info = &model.ActorInfo{
		ID:       id,
		Provider: avl.Name(),
		Homepage: detailURL,
		Aliases:  []string{},
		Images:   []string{},
	}
	c := avl.ClonedCollector()
	c.SetRequestTimeout(30 * time.Second)

	// 头像。
	c.OnXML(`//div[contains(@class,"actress-image")]//img`, func(e *colly.XMLElement) {
		if src := e.Request.AbsoluteURL(e.Attr("src")); src != "" {
			info.Images = append(info.Images, src)
		}
	})

	// 简介（排除作品数统计段落 p.count）。
	c.OnXML(`//div[contains(@class,"archive-description")]/p[not(contains(@class,"count"))]`, func(e *colly.XMLElement) {
		if text := strings.TrimSpace(e.Text); text != "" {
			if info.Summary == "" {
				info.Summary = text
			} else {
				info.Summary += "\n" + text
			}
		}
	})

	// 各字段（dt/dd 成对，取每个 dd 紧邻的前一个 dt 作为字段名）。
	c.OnXML(`//div[contains(@class,"actress-data")]/dl/dd`, func(e *colly.XMLElement) {
		label := strings.TrimSpace(e.ChildText(`preceding-sibling::dt[1]`))
		label = strings.Trim(label, "：: 　")
		value := strings.TrimSpace(e.Text)
		switch label {
		case "AV女優名", "AV男優名":
			// 形如：三上悠亜（みかみゆあ）- yua mikami
			if name := parseWikiActorName(value); name != "" {
				info.Name = name
			}
		case "別名義":
			info.Aliases = append(info.Aliases, parseWikiAliases(value)...)
		case "生年月日":
			info.Birthday = parser.ParseDate(value)
		case "サイズ":
			parseWikiSize(value, info)
		}
	})

	// SNS 链接（actress-data 内仅 SNS 的 dd 含链接）。
	c.OnXML(`//div[contains(@class,"actress-data")]/dl/dd//a`, func(e *colly.XMLElement) {
		u, pErr := url.Parse(e.Attr("href"))
		if pErr != nil {
			return
		}
		segments := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(segments) == 0 || segments[0] == "" {
			return
		}
		handle := segments[0]
		host := strings.ToLower(u.Host)
		switch {
		case strings.Contains(host, "twitter.com") || strings.HasSuffix(host, "x.com"):
			if info.Twitter == "" {
				info.Twitter = handle
			}
		case strings.Contains(host, "instagram.com"):
			if info.Instagram == "" {
				info.Instagram = handle
			}
		}
	})

	if err = c.Visit(detailURL); err != nil {
		return nil, err
	}
	if info.Name == "" {
		return nil, provider.ErrInfoNotFound
	}
	return info, nil
}

// parseWikiActorID 从演员详情页地址解析 ID（URL 最后一段 slug）。
// 例如：https://av-wiki.net/av-actress/mikami-yua/ -> mikami-yua
func parseWikiActorID(rawURL string) (id string, err error) {
	homepage, err := url.Parse(rawURL)
	if err != nil {
		return
	}
	return path.Base(strings.TrimRight(homepage.Path, "/")), nil
}

// parseWikiActorName 提取 av-wiki「AV女優名」中的主名字。
// 输入形如：三上悠亜（みかみゆあ）- yua mikami
func parseWikiActorName(s string) string {
	s = strings.TrimSpace(s)
	// 主名字在日文括号（读音）之前。
	if i := strings.IndexAny(s, "（("); i >= 0 {
		s = s[:i]
	}
	// 没有括号时，罗马音以 "-" 分隔。
	if i := strings.Index(s, "-"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// parseWikiAliases 解析 av-wiki「別名義」。
// 输入形如：天海こころ（あまみこころ）・深田 詠美（中華圏），
// 或速美もな（はやみもな）、葉里さやか（…）；「– – –」表示无别名。
// 别名之间可能用「・」或「、」分隔。
func parseWikiAliases(s string) []string {
	var aliases []string
	for _, item := range regexp.MustCompile(`[・、]`).Split(s, -1) {
		alias := parseWikiActorName(item)
		alias = strings.TrimSpace(alias)
		if alias == "" || strings.Trim(alias, "-–—− ") == "" {
			continue // 空或仅为占位横线。
		}
		aliases = append(aliases, alias)
	}
	return aliases
}

// wikiCupRe 匹配「サイズ」里跟在 B 值后面的罩杯（部分演员才公布）：
// Dカップ / D / （Ｆカップ）/ AAカップ。
var wikiCupRe = regexp.MustCompile(`[（(]\s*([A-Za-zＡ-Ｚａ-ｚ]{1,2})\s*(?:カップ)?\s*[）)]`)

// normalizeWikiCup 罩杯统一成半角大写（页面偶尔会用全角字母写罩杯）。
func normalizeWikiCup(cup string) string {
	return strings.ToUpper(strings.Map(func(r rune) rune {
		switch {
		case r >= 'Ａ' && r <= 'Ｚ':
			return r - 'Ａ' + 'A'
		case r >= 'ａ' && r <= 'ｚ':
			return r - 'ａ' + 'a'
		default:
			return r
		}
	}, cup))
}

// parseWikiSize 解析 av-wiki「サイズ」，形如：
//
//	T159-B83-W57-H88
//	T160cm B84cm(Dカップ) W56cm H88cm
//	T160-B88(F)-W54-H92
//
// T=身高，B=胸围，W=腰围，H=臀围（数值可能含小数、可能带 cm、可能是「〇」占位），
// 罩杯（仅部分演员公布）写在 B 值后面的括号里，可能带「カップ」后缀。
func parseWikiSize(s string, info *model.ActorInfo) {
	re := regexp.MustCompile(`([TBWH])\s*(\d+(?:\.\d+)?)`)
	var height, b, w, h float64
	for _, m := range re.FindAllStringSubmatch(s, -1) {
		v, _ := strconv.ParseFloat(m[2], 64)
		switch m[1] {
		case "T":
			height = v
		case "B":
			b = v
		case "W":
			w = v
		case "H":
			h = v
		}
	}
	if height > 0 {
		info.Height = int(height + 0.5) // 四舍五入。
	}
	if b > 0 || w > 0 || h > 0 {
		f := func(v float64) string {
			if v <= 0 {
				return "-"
			}
			return strconv.Itoa(int(v + 0.5))
		}
		info.Measurements = fmt.Sprintf("B:%s / W:%s / H:%s", f(b), f(w), f(h))
	}
	if m := wikiCupRe.FindStringSubmatch(s); len(m) == 2 {
		info.CupSize = normalizeWikiCup(m[1])
	}
}

// mergeActorInfoFromWiki 将 av-wiki 的数据合并进结果，只补全缺失字段，
// 不覆盖已有的（av-league）数据。
func mergeActorInfoFromWiki(dst, src *model.ActorInfo) {
	if dst.Name == "" {
		dst.Name = src.Name
	}
	if dst.Height == 0 {
		dst.Height = src.Height
	}
	if time.Time(dst.Birthday).IsZero() {
		dst.Birthday = src.Birthday
	}
	if dst.Measurements == "" {
		dst.Measurements = src.Measurements
	}
	if dst.CupSize == "" {
		dst.CupSize = src.CupSize
	}
	if dst.BloodType == "" {
		dst.BloodType = src.BloodType
	}
	if dst.Nationality == "" {
		dst.Nationality = src.Nationality
	}
	if dst.Twitter == "" {
		dst.Twitter = src.Twitter
	}
	if dst.Instagram == "" {
		dst.Instagram = src.Instagram
	}
	if dst.Summary == "" {
		dst.Summary = src.Summary
	}
	if len(dst.Images) == 0 {
		dst.Images = src.Images
	}
	// 别名合并去重。
	dst.Aliases = mergeUniqueStrings(dst.Aliases, src.Aliases)
}

// mergeUniqueStrings 合并两个字符串切片并去重，保留首次出现顺序。
func mergeUniqueStrings(a, b []string) []string {
	seen := make(map[string]struct{}, len(a)+len(b))
	res := make([]string, 0, len(a)+len(b))
	for _, s := range append(append([]string{}, a...), b...) {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		res = append(res, s)
	}
	return res
}

// ===========================================================

// lj-兼容没有BWH的场景，也能取到cup
func parseMeasurements(s string) (B, W, H int, Cup string) {
	for _, item := range strings.Split(s, "/") {
		name, data, found := strings.Cut(item, ":")
		if !found {
			continue
		}
		trimmedData := strings.TrimSpace(data)
		switch strings.TrimSpace(name) {
		// B: - （C） / W: - / H: -
		case "B":
			// 改进版：将 \d+ 改为 [-\d]*，表示匹配数字或减号   兼容B: - （A） / W: - / H: -
			if ss := regexp.MustCompile(`([-−\d]+)(?:\s*[（(]([A-Z]?)[）)])?`).FindStringSubmatch(data); len(ss) == 3 {
				B = parser.ParseInt(ss[1])
				Cup = ss[2]
			}
		case "W":
			W = parser.ParseInt(trimmedData)
		case "H":
			H = parser.ParseInt(trimmedData)
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

// parseSNSUsername 解析 av-league 演员页「Twitter / インスタ」行里的账号名。
//
// 优先读取超链接的 href：站方写到链接文本里的名字常常是坏的，例如
// https://www.av-league.com/actress/37518.html 的 X 显示为
// "https:x.comnatsuki_rin123"，而 href 是准确的 https://x.com/natsuki_rin123。
// 只有在链接不可用时，才退回解析文本。
func parseSNSUsername(e *colly.XMLElement, text string, domains ...string) string {
	if username := usernameFromSNSURL(e.ChildAttr(`.//td//a`, "href"), domains...); username != "" {
		return username
	}
	return usernameFromSNSText(text)
}

// usernameFromSNSURL 从 SNS 链接里取账号名（URL 路径的第一段），
// 域名不属于 domains 时返回空字符串。
func usernameFromSNSURL(rawURL string, domains ...string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Host == "" {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	matched := false
	for _, domain := range domains {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			matched = true
			break
		}
	}
	if !matched {
		return ""
	}
	return firstURLPathSegment(u.Path)
}

// firstURLPathSegment 取 URL 路径的第一段非空内容，并去掉可能存在的 "@"。
func firstURLPathSegment(p string) string {
	for _, segment := range strings.Split(strings.Trim(p, "/"), "/") {
		if segment = strings.TrimPrefix(strings.TrimSpace(segment), "@"); segment != "" {
			return segment
		}
	}
	return ""
}

// snsTextUsernameRe 匹配写坏的 SNS 文本，例如：
// "https:x.comnatsuki_rin123"（缺少 // 和域名后的 /）、
// "https://twitter.com/aiiro_nagi"、"https://www.instagram.com/natsu_kirinnn/"。
var snsTextUsernameRe = regexp.MustCompile(
	`(?i)^(?:https?:)?(?://)?(?:www\.)?(?:x\.com|twitter\.com|instagram\.com)/*(.*)$`)

// usernameFromSNSText 从行文本里解析账号名（链接不可用时的兜底）。
func usernameFromSNSText(text string) string {
	text = strings.TrimSpace(strings.Trim(strings.TrimSpace(text), `"`))
	if m := snsTextUsernameRe.FindStringSubmatch(text); len(m) == 2 {
		return strings.Trim(strings.TrimSpace(m[1]), "/")
	}
	return strings.TrimPrefix(text, "@")
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
