package badge

import (
	"fmt"
	"image"
	"time"

	"github.com/jellydator/ttlcache/v3"

	"github.com/metatube-community/metatube-sdk-go/common/fetch"
	"github.com/metatube-community/metatube-sdk-go/imageutil"
)

var (
	badgeCache = ttlcache.New[string, image.Image](
		ttlcache.WithTTL[string, image.Image](30*time.Minute),
		ttlcache.WithCapacity[string, image.Image](10),
	)
	badgeFetcher = fetch.Default(nil)
)

func init() {
	// start badge cache.
	go badgeCache.Start()
}

func Badge(src image.Image, badge string) (image.Image, error) {
	img, i, err := getBadgeFromCache(badge)
	if err != nil {
		return i, err
	}
	wmk := imageutil.Resize(img, 0, src.Bounds().Dy()/5 /* 0.2 */)
	return imageutil.Watermark(src, wmk, image.Point{}), nil
}

func getBadgeFromCache(badge string) (image.Image, image.Image, error) {
	var img image.Image
	if item := badgeCache.Get(badge); item != nil {
		img = item.Value()
	} else {
		resp, err := badgeFetcher.Fetch(badge)
		if err != nil {
			return nil, nil, fmt.Errorf("fetch badge: %w", err)
		}
		defer resp.Body.Close()
		// decode badge image.
		img, _, err = imageutil.Decode(resp.Body)
		if err != nil {
			return nil, nil, fmt.Errorf("decode badge: %w", err)
		}
		badgeCache.Set(badge, img, ttlcache.DefaultTTL)
	}
	return img, nil, nil
}

func Wuma(src image.Image) (image.Image, error) {
	img, i, err := getBadgeFromCache("wuma.png")
	if err != nil {
		return i, err
	}
	wmk := imageutil.Resize(img, 0, src.Bounds().Dy()/5)

	// 计算右上角坐标：
	// X = 主图最右侧边界 - 标签宽度
	// Y = 0 (贴顶)
	rightTopPos := image.Point{
		X: src.Bounds().Max.X - wmk.Bounds().Dx(),
		Y: 0,
	}

	return imageutil.Watermark(src, wmk, rightTopPos), nil
}
