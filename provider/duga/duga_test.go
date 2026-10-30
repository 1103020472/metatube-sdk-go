package duga

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/1103020472/metatube-sdk-go/model"
	"github.com/1103020472/metatube-sdk-go/provider/internal/testkit"
)

func TestDUGA_GetMovieInfoByID(t *testing.T) {
	testkit.Test(t, New, []string{
		"glory-4262",
		"waap-1294",
	})
}

func TestDUGA_SearchMovie(t *testing.T) {
	testkit.Test(t, New, []string{
		"DINM",
	})
}

func TestDUGA_GetMovieReviewsByID(t *testing.T) {
	testkit.Test(t, New, []string{
		"maniac-0031", // 15 reviews, spans two pages.
	})
}

func TestDUGA_GetMovieReviewsByURL(t *testing.T) {
	testkit.Test(t, New, []string{
		"https://duga.jp/ppv/glory-4262/",
	})
}

func TestReviewTotalRegexp(t *testing.T) {
	for _, c := range []struct {
		in   string
		want string
	}{
		{"1～10件 / 全15件", "15"},
		{"1～1件 / 全1件", "1"},
		{"1～3件 / 全 3 件", "3"},
	} {
		ss := reviewTotalRegexp.FindStringSubmatch(c.in)
		assert.Len(t, ss, 2, c.in)
		assert.Equal(t, c.want, ss[1], c.in)
	}
}

func TestCanonicalNumber(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"MIZD-398", "mizd398"},
		{"mizd0398", "mizd398"},
		{"DSVR-0030", "dsvr30"},
		{"dsvr_30", "dsvr30"},
		{"31ID-036", "31id36"},
		{"tma2-0526", "tma2526"},
		{"T28-643", "t28643"},
	} {
		assert.Equal(t, c.want, canonicalNumber(c.in), c.in)
	}
}

func TestMatchesNumber(t *testing.T) {
	newResult := func(id, number string) *model.MovieSearchResult {
		return &model.MovieSearchResult{ID: id, Number: number}
	}

	// The real search result for "31ID-036" is stored under another ID.
	assert.True(t, matchesNumber(newResult("tma2-0526", "31ID-036"), "31ID-036"))
	assert.True(t, matchesNumber(newResult("tma2-0526", "31ID-036"), "tma2-0526"))
	assert.True(t, matchesNumber(newResult("tma2-0526", "31ID-036"), "TMA2-526"))

	// Recommendation entries never match the queried number.
	assert.False(t, matchesNumber(newResult("tma2-0592", "32ID-037"), "MIZD-398"))
	assert.False(t, matchesNumber(newResult("tma2-0592", "32ID-037"), "ABCD-999"))

	// Keywords that are not a complete number are never filtered.
	assert.True(t, matchesNumber(newResult("tma2-0592", "32ID-037"), "巨乳"))
	assert.True(t, matchesNumber(newResult("tma2-0592", "32ID-037"), "DINM"))
}
