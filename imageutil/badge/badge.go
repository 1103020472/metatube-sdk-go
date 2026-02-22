package badge

import (
	"fmt"
	"image"
	"time"

	"github.com/jellydator/ttlcache/v3"

	"github.com/1103020472/metatube-sdk-go/common/fetch"
	"github.com/1103020472/metatube-sdk-go/imageutil"
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
	leftTopPos := image.Point{
		X: src.Bounds().Min.X,
		Y: src.Bounds().Min.Y,
	}
	return imageutil.Watermark(src, wmk, leftTopPos), nil
}

func Umr(src image.Image, umr string) (image.Image, error) {
	img, i, err := getBadgeFromCache(umr)
	if err != nil {
		return i, err
	}
	wmk := imageutil.Resize(img, 0, src.Bounds().Dy()/10)

	// 重要：如果 src.Bounds().Min.X 不是 0，
	// 那么真正的右边缘坐标是 src.Bounds().Max.X，而不是 Dx()
	rightTopPos := image.Point{
		X: src.Bounds().Min.X,
		Y: src.Bounds().Max.Y - wmk.Bounds().Dy(),
	}

	return imageutil.Watermark(src, wmk, rightTopPos), nil
}

func getBadgeFromCache(badge string) (image.Image, image.Image, error) {
	var img image.Image
	item := badgeCache.Get(badge)
	if item != nil {
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
