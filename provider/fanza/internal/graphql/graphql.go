package graphql

import (
	"context"
	_ "embed"
	"errors"
	"reflect"

	"github.com/machinebox/graphql"
)

const (
	videoURL   = "https://video.dmm.co.jp/"
	graphqlURL = "https://api.video.dmm.co.jp/graphql"
)

var (
	//go:embed query/ContentPageData.graphql
	contentPageDataQuery string

	//go:embed query/UserReviews.graphql
	userReviewsQuery string
)

var ErrNullResponse = errors.New("response is null")

type ClientOption = graphql.ClientOption

var (
	WithHTTPClient   = graphql.WithHTTPClient
	UseMultipartForm = graphql.UseMultipartForm
)

type Client struct {
	gc *graphql.Client
}

func NewClient(opts ...ClientOption) *Client {
	return &Client{
		gc: graphql.NewClient(graphqlURL, opts...),
	}
}

func (c *Client) GetContentPageData(id string, opts ContentPageDataQueryOptions) (*ContentPageDataResponse, error) {
	req := graphql.NewRequest(contentPageDataQuery)
	req.Var("id", id)
	req.Var("isLoggedIn", opts.IsLoggedIn)
	req.Var("isAmateur", opts.IsAmateur)
	req.Var("isAnime", opts.IsAnime)
	req.Var("isAv", opts.IsAv)
	req.Var("isCinema", opts.IsCinema)
	req.Var("isSP", opts.IsSP)
	req.Var("shouldFetchRelatedTags", true)

	req.Header.Set("Referer", videoURL)
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Fanza-Device", "BROWSER")
	req.Header.Set("User-Agent", "") // skip

	var resp ContentPageDataResponse
	if err := c.gc.Run(context.Background(), req, &resp); err != nil {
		return nil, err
	}

	if reflect.DeepEqual(resp, ContentPageDataResponse{Typename: resp.Typename}) {
		return nil, ErrNullResponse
	}

	return &resp, nil
}

func (c *Client) GetUserReviews(id string, offset ...int) (*UserReviewsResponse, error) {
	req := graphql.NewRequest(userReviewsQuery)
	req.Var("id", id)
	req.Var("sort", "HELPFUL_COUNT_DESC")
	req.Var("offset", 0) // default offset
	if len(offset) > 0 {
		req.Var("offset", offset[0])
	}

	req.Header.Set("Referer", videoURL)
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Fanza-Device", "BROWSER")
	req.Header.Set("User-Agent", "") // skip

	var resp UserReviewsResponse
	if err := c.gc.Run(context.Background(), req, &resp); err != nil {
		return nil, err
	}

	if reflect.DeepEqual(resp, UserReviewsResponse{}) {
		return nil, ErrNullResponse
	}

	return &resp, nil
}

// ===== 补充 TopSearch 响应体结构 =====
type TopSearchResponse struct {
	LegacySearchPPV struct {
		Result struct {
			Contents []struct {
				ID           string `json:"id"`
				Title        string `json:"title"`
				Floor        string `json:"floor"`
				PackageImage struct {
					MediumUrl string `json:"mediumUrl"`
					LargeUrl  string `json:"largeUrl"`
				} `json:"packageImage"`
				Review struct {
					Average float64 `json:"average"`
				} `json:"review"`
				DeliveryStartAt string `json:"deliveryStartAt"`
				Actresses       []struct {
					Name string `json:"name"`
				} `json:"actresses"`
			} `json:"contents"`
		} `json:"result"`
	} `json:"legacySearchPPV"`
}

// 提取你抓包请求中的核心 GraphQL Query，省略了非必要的 Fragment 减小体积
const topSearchQuery = `query TopSearch($limit: Int!, $offset: Int, $floor: PPVFloor, $sort: ContentSearchPPVSort!, $queryWord: String, $filter: ContentSearchPPVFilterInput, $legacyProductType: LegacyProductType = DOWNLOAD, $isLoggedIn: Boolean!, $facetLimit: Int!, $hasLegacyProductType: Boolean = false, $excludeUndelivered: Boolean!, $shouldGetBookmark: Boolean!, $isListUiAbTestTarget: Boolean = false, $shouldFetchFanzaClipCount: Boolean!, $guestToken: String!) {
  legacySearchPPV(limit: $limit, offset: $offset, floor: $floor, sort: $sort, queryWord: $queryWord, filter: $filter, facetLimit: $facetLimit, includeExplicit: true, excludeUndelivered: $excludeUndelivered) {
    result {
      contents {
        id
        title
        floor
        packageImage {
          mediumUrl
          largeUrl
        }
        review {
          average
        }
        deliveryStartAt
        actresses {
          name
        }
      }
    }
  }
}`

// ===== 新增 TopSearch 方法 =====
func (c *Client) TopSearch(keyword string) (*TopSearchResponse, error) {
	req := graphql.NewRequest(topSearchQuery)
	// 根据 curl 对应的请求体设置变量
	req.Var("excludeUndelivered", true)
	req.Var("facetLimit", 4)
	req.Var("guestToken", "")
	req.Var("isListUiAbTestTarget", false)
	req.Var("isLoggedIn", false)
	req.Var("limit", 120)
	req.Var("offset", 0)
	req.Var("queryWord", keyword)
	req.Var("shouldFetchFanzaClipCount", false)
	req.Var("shouldGetBookmark", false)
	req.Var("sort", "DELIVERY_START_DATE") // 按日期排序

	req.Header.Set("Referer", videoURL)
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Fanza-Device", "BROWSER")

	var resp TopSearchResponse
	if err := c.gc.Run(context.Background(), req, &resp); err != nil {
		return nil, err
	}

	return &resp, nil
}
