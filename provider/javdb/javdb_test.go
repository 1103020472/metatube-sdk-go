package javdb

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/gocolly/colly/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/1103020472/metatube-sdk-go/internal/envconfig"
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

func TestJavDBIsLoginRedirect(t *testing.T) {
	response := func(rawURL string) *colly.Response {
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
