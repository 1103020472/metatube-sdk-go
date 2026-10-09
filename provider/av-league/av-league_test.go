package avleague

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/1103020472/metatube-sdk-go/model"
	"github.com/1103020472/metatube-sdk-go/provider/internal/testkit"
)

// TestParseWikiSize av-wiki「サイズ」的解析（含只有部分演员公布的罩杯）。
func TestParseWikiSize(t *testing.T) {
	cases := []struct {
		size         string
		height       int
		measurements string
		cup          string
	}{
		{"T159-B83-W57-H88", 159, "B:83 / W:57 / H:88", ""},
		{"T160cm B84cm(Dカップ) W56cm H88cm", 160, "B:84 / W:56 / H:88", "D"},
		{"T173 / B87(Gカップ) / W60 / H94", 173, "B:87 / W:60 / H:94", "G"},
		{"T160-B88(F)-W54-H92", 160, "B:88 / W:54 / H:92", "F"},
		{"T153 B95(G) W65 H90", 153, "B:95 / W:65 / H:90", "G"},
		{"T161 / B110(Oカップ) / W65 / H92", 161, "B:110 / W:65 / H:92", "O"},
		{"T155 / B92（ｆカップ） / W59 / H89", 155, "B:92 / W:59 / H:89", "F"},
		{"T〇-B〇-W〇-H〇", 0, "", ""}, // 占位符不解析。
		{"", 0, "", ""},
	}

	for _, c := range cases {
		info := &model.ActorInfo{}
		parseWikiSize(c.size, info)
		assert.Equalf(t, c.height, info.Height, "height of %q", c.size)
		assert.Equalf(t, c.measurements, info.Measurements, "measurements of %q", c.size)
		assert.Equalf(t, c.cup, info.CupSize, "cup of %q", c.size)
	}
}

func TestAVLeague_GetActorInfoByID(t *testing.T) {
	testkit.Test(t, New, []string{
		"8301",
		"14005",
		"36672",
		"32759",
		"36736",
	})
}

func TestAVLeague_GetActorInfoByURL(t *testing.T) {
	testkit.Test(t, New, []string{
		// av-league 演员页：Twitter / Instagram 取自超链接，
		// 并合并 av-wiki 的简介（summary）。
		"https://www.av-league.com/actress/8301.html",
		// av-wiki 演员页（av-league 没有的演员）。
		"https://av-wiki.net/av-actress/minaduki-hikaru/",
	})
}

func TestAVLeague_SearchActor(t *testing.T) {
	testkit.Test(t, New, []string{
		"白川ゆず",
		"美竹すず",
		"宇流木さら",
	})
}
