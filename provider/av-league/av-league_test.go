package avleague

import (
	"testing"

	"github.com/1103020472/metatube-sdk-go/provider/internal/testkit"
)

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
