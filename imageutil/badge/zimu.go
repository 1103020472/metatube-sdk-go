package badge

import (
	"bytes"
	_ "embed"

	"github.com/jellydator/ttlcache/v3"

	"github.com/metatube-community/metatube-sdk-go/imageutil"
)

//go:embed zimu.png
var zimu []byte
var wuma []byte

func init() {
	badge, _, _ := imageutil.Decode(bytes.NewReader(zimu))
	badgeCache.Set("zimu.png", badge, ttlcache.NoTTL)

	wuma, _, _ := imageutil.Decode(bytes.NewReader(wuma))
	badgeCache.Set("wuma.png", wuma, ttlcache.NoTTL)
}
