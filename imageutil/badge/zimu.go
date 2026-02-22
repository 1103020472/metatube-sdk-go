package badge

import (
	"bytes"
	_ "embed"

	"github.com/jellydator/ttlcache/v3"

	"github.com/1103020472/metatube-sdk-go/imageutil"
)

//go:embed zimu.png
var zimu []byte

//go:embed umr.png
var umr []byte

func init() {
	badge, _, _ := imageutil.Decode(bytes.NewReader(zimu))
	badgeCache.Set("zimu.png", badge, ttlcache.NoTTL)

	umr, _, _ := imageutil.Decode(bytes.NewReader(umr))
	badgeCache.Set("umr.png", umr, ttlcache.NoTTL)
}
