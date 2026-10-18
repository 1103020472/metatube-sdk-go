package javdb

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/gocolly/colly/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/1103020472/metatube-sdk-go/internal/envconfig"
	"github.com/1103020472/metatube-sdk-go/model"
	sdkerrors "github.com/1103020472/metatube-sdk-go/errors"
	"github.com/1103020472/metatube-sdk-go/provider/internal/testkit"
)

func TestJavDB_GetMovieInfoByID(t *testing.T) {
	testkit.Test(t, New, []string{
		// Movie hashes (native JavDB IDs).
		"Mb4Dbv",
		"ZY5eq",
		// Movie numbers, resolved through search.
		"IPVR-328",
		"SSIS-001",
		"MDVR-411",
		// JavDB lists this one as "LUXU-1234".
		"259LUXU-1234",
	})
}

func TestJavDB_GetMovieInfoByURL(t *testing.T) {
	testkit.Test(t, New, []string{
		"https://javdb.com/v/Mb4Dbv",
		"https://javdb.com/v/ZY5eq",
	})
}

func TestJavDB_SearchMovie(t *testing.T) {
	testkit.Test(t, New, []string{
		"IPVR-328",
		"SSIS-001",
		"3DSVR00123",
		"MDVR-411",
		"259LUXU-1234",
	})
}

func TestJavDBParseMovieIDFromURL(t *testing.T) {
	javdb := New()
	for raw, expected := range map[string]string{
		"https://javdb.com/v/Mb4Dbv":                 "Mb4Dbv",
		"https://javdb.com/v/Mb4Dbv/reviews/lastest": "Mb4Dbv",
		"https://javdb.com/v/4vQJb?locale=zh":        "4vQJb",
	} {
		id, err := javdb.ParseMovieIDFromURL(raw)
		assert.NoError(t, err)
		assert.Equal(t, expected, id)
	}
	for _, raw := range []string{
		"https://javdb.com/",
		"https://javdb.com/search?q=IPVR-328&f=all",
		"https://javdb.com/actors/0dKX",
	} {
		_, err := javdb.ParseMovieIDFromURL(raw)
		assert.Error(t, err, raw)
	}
}

func TestJavDBNormalizeMovieKeyword(t *testing.T) {
	javdb := New()
	for keyword, expected := range map[string]string{
		"ipvr-328":        "IPVR-328",
		"SSIS-001":        "SSIS-001",
		"3DSVR00123":      "DSVR-123",
		"3DSVR-00123":     "DSVR-123",
		"dsvr00123":       "DSVR-123",
		"VRKM00123":       "VRKM-123",
		"FC2-PPV-1234567": "FC2-PPV-1234567",
	} {
		assert.Equal(t, expected, javdb.NormalizeMovieKeyword(keyword), keyword)
	}
}

func TestJavDBParseScore(t *testing.T) {
	assert.Equal(t, 4.32, parseScore("4.32分, 由195人評價"))
	assert.Equal(t, 4.0, parseScore("4分"))
	assert.Equal(t, 0.0, parseScore("暫時沒有評分"))
	assert.Equal(t, 0.0, parseScore("195人評價"))
}

func TestJavDBNormalizeNumber(t *testing.T) {
	assert.Equal(t, "IPVR328", normalizeNumber("ipvr-328"))
	assert.Equal(t, "IPVR328", normalizeNumber("IPVR_328"))
	assert.Equal(t, "3DSVR123", normalizeNumber("3DSVR-123"))
}

func TestJavDBTrailingDigits(t *testing.T) {
	assert.Equal(t, "1234", trailingDigits("LUXU-1234"))
	assert.Equal(t, "1234", trailingDigits("259LUXU-1234"))
	assert.Equal(t, "1234567", trailingDigits("FC2-PPV-1234567"))
	assert.Equal(t, "123", trailingDigits("3DSVR00123"))
	assert.Equal(t, "0", trailingDigits("HEYZO-000"))
	assert.Equal(t, "", trailingDigits("MDVR"))
}

func TestJavDBLettersOnly(t *testing.T) {
	assert.Equal(t, "FCPPV", lettersOnly("FC2-PPV-1234567"))
	assert.Equal(t, "LUXU", lettersOnly("259LUXU-1234"))
	assert.Equal(t, "MDVR", lettersOnly("mdvr-411"))
	assert.Equal(t, "", lettersOnly("123-456"))
}

func TestJavDBSameNumber(t *testing.T) {
	for _, tt := range []struct {
		a, b  string
		match bool
	}{
		{`MDVR-411`, `MDVR-411`, true},
		{`mdvr-411`, `MDVR411`, true},
		{`3DSVR00123`, `DSVR-123`, true},
		// JavDB drops some label prefixes.
		{`259LUXU-1234`, `LUXU-1234`, true},
		{`FC2-PPV-1234567`, `FC2-1234567`, true},
		// JavDB's fuzzy neighbours must not match.
		{`MDVR-411`, `MDVR-441`, false},
		{`MDVR-411`, `MDVR-419`, false},
		{`SSIS-001`, `SSIS-101`, false},
		{`259LUXU-1234`, `LUXU-1230`, false},
		{`FC2-PPV-1234567`, `FC2-1234560`, false},
		{`MDVR-411`, `MIDA-411`, false},
	} {
		assert.Equal(t, tt.match, sameNumber(tt.a, tt.b), "%s vs %s", tt.a, tt.b)
		assert.Equal(t, tt.match, sameNumber(tt.b, tt.a), "%s vs %s", tt.b, tt.a)
	}
}

func TestJavDBFullNumberRegexp(t *testing.T) {
	for _, s := range []string{
		"MDVR-411", "MDVR411", "SSIS-001", "IPVR328",
		"259LUXU-1234", "FC2-PPV-1234567", "DSVR-123", "TOKYO-HOT-1234",
	} {
		assert.True(t, fullNumberRegexp.MatchString(s), s)
	}
	// Titles must never be treated as a movie number.
	for _, s := range []string{
		"", "MDVR", "2SEX", "M男専用大人保育園", "SSIS-001 エロ配信",
		"本田岬", "259LUXU", "MDVR-",
	} {
		assert.False(t, fullNumberRegexp.MatchString(s), s)
	}
}

// searchResult builds the search results used by the filtering tests.
func searchResult(id, number string) *model.MovieSearchResult {
	return &model.MovieSearchResult{
		ID:       id,
		Number:   number,
		Provider: Name,
		Homepage: fmt.Sprintf(movieURL, id),
	}
}

func TestJavDBFilterResultsByNumber(t *testing.T) {
	// The first results of https://javdb.com/search?q=MDVR-411&f=all.
	results := []*model.MovieSearchResult{
		searchResult("82ZKA3", "MDVR-411"),
		searchResult("NQmqBN", "MDVR-441"),
		searchResult("0e2WRJ", "MDVR-421"),
		searchResult("Mb7XV1", "MDVR-419"),
		searchResult("Mb21J", "MDVR-011"),
	}

	filtered := filterResultsByNumber(results, "MDVR-411")
	require.Len(t, filtered, 1)
	assert.Equal(t, "MDVR-411", filtered[0].Number)

	// An exact match wins over JavDB's rewritten ones.
	filtered = filterResultsByNumber(results, "MDVR-011")
	require.Len(t, filtered, 1)
	assert.Equal(t, "MDVR-011", filtered[0].Number)

	// A title search is fuzzy by nature and must not be filtered.
	assert.Nil(t, filterResultsByNumber(results, "MDVR 411 エロ配信"))
	assert.Nil(t, filterResultsByNumber(results, "MDVR"))
	// Nothing related, so the caller falls back to the raw results.
	assert.Nil(t, filterResultsByNumber(results, "SSIS-001"))
}

func TestJavDBFilterResultsByNumberWithRewrites(t *testing.T) {
	results := []*model.MovieSearchResult{
		searchResult("mbQOd", "LUXU-1234"),
		searchResult("abcde", "LUXU-1230"),
		searchResult("fghij", "LUXU-1232"),
	}
	filtered := filterResultsByNumber(results, "259LUXU-1234")
	require.Len(t, filtered, 1)
	assert.Equal(t, "LUXU-1234", filtered[0].Number)

	results = []*model.MovieSearchResult{
		searchResult("rezDN", "FC2-1234567"),
		searchResult("3ENE9", "FC2-1234560"),
	}
	filtered = filterResultsByNumber(results, "FC2-PPV-1234567")
	require.Len(t, filtered, 1)
	assert.Equal(t, "FC2-1234567", filtered[0].Number)
}

func TestJavDBFindResultByNumber(t *testing.T) {
	results := []*model.MovieSearchResult{
		searchResult("mbQOd", "LUXU-1234"),
	}
	// Exact lookup fails, the tolerant one succeeds.
	assert.Nil(t, findResultByNumber(results, "259LUXU-1234", true))
	assert.NotNil(t, findResultByNumber(results, "259LUXU-1234", false))
	assert.Nil(t, findResultByNumber(results, "LUXU-1230", false))
}

func TestJavDBIsLoginRedirect(t *testing.T) {	response := func(rawURL string) *colly.Response {
		u, err := url.Parse(rawURL)
		assert.NoError(t, err)
		return &colly.Response{Request: &colly.Request{URL: u}}
	}
	assert.True(t, isLoginRedirect(response("https://javdb.com/login")))
	assert.False(t, isLoginRedirect(response("https://javdb.com/v/Mb4Dbv")))
	assert.False(t, isLoginRedirect(&colly.Response{}))
	assert.False(t, isLoginRedirect(nil))
}

func TestJavDBIsCloudflareChallenge(t *testing.T) {
	headers := func(key, value string) *http.Header {
		h := http.Header{}
		if key != "" {
			h.Set(key, value)
		}
		return &h
	}
	assert.True(t, isCloudflareChallenge(&colly.Response{
		StatusCode: http.StatusForbidden, Headers: headers("", ""),
	}))
	assert.True(t, isCloudflareChallenge(&colly.Response{
		StatusCode: http.StatusOK, Headers: headers("", ""),
		Body: []byte("<html><title>Just a moment...</title></html>"),
	}))
	assert.True(t, isCloudflareChallenge(&colly.Response{
		StatusCode: http.StatusOK, Headers: headers("Cf-Mitigated", "challenge"),
	}))
	assert.False(t, isCloudflareChallenge(&colly.Response{
		StatusCode: http.StatusOK, Headers: headers("", ""),
		Body: []byte("<html><body>JavDB</body></html>"),
	}))
	assert.False(t, isCloudflareChallenge(nil))
}

func TestJavDBSetConfigCookie(t *testing.T) {
	var cookie string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie = r.Header.Get("Cookie")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<html><body>ok</body></html>"))
	}))
	defer server.Close()

	javdb := New()
	// No cookie configured, nothing must be sent.
	require.NoError(t, javdb.collector().Visit(server.URL))
	assert.NotContains(t, cookie, "_jdb_session")

	config := envconfig.NewConfig()
	config.Set("cookie", "_jdb_session=abc123")
	require.NoError(t, javdb.SetConfig(config))
	require.NoError(t, javdb.collector().Visit(server.URL))
	assert.Contains(t, cookie, "_jdb_session=abc123")
}

func TestJavDBResponseErrorLoginRequired(t *testing.T) {
	javdb := New()
	response := func(rawURL string) *colly.Response {
		u, err := url.Parse(rawURL)
		require.NoError(t, err)
		return &colly.Response{
			Request:    &colly.Request{URL: u},
			StatusCode: http.StatusOK,
			Headers:    &http.Header{},
		}
	}

	// Without a cookie: "login required" (setup hint).
	err := javdb.responseError(response("https://javdb.com/login"))
	require.Error(t, err)
	var httpErr *sdkerrors.HTTPError
	require.ErrorAs(t, err, &httpErr)
	assert.Equal(t, http.StatusUnauthorized, httpErr.Code)
	assert.Contains(t, httpErr.Message, "需要登录")
	assert.Contains(t, httpErr.Message, "cookie")

	// With an expired cookie configured: "cookie expired" hint.
	config := envconfig.NewConfig()
	config.Set("cookie", "_jdb_session=expired")
	require.NoError(t, javdb.SetConfig(config))
	err = javdb.responseError(response("https://javdb.com/login"))
	require.Error(t, err)
	require.ErrorAs(t, err, &httpErr)
	assert.Contains(t, httpErr.Message, "cookie 已失效")

	// A normal page is not an error.
	assert.NoError(t, javdb.responseError(response("https://javdb.com/v/Mb4Dbv")))
}
