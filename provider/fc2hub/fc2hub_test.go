package fc2hub

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/1103020472/metatube-sdk-go/internal/envconfig"
	"github.com/1103020472/metatube-sdk-go/provider/internal/testkit"
)

func TestFC2HUB_GetMovieInfoByID(t *testing.T) {
	testkit.Test(t, New, []string{
		"1152468-2725031",
		"1258474-3104947",
		"1258463-3104926",
		"1258427-3104805",
		"1258427-3104805",
		"230929-803681",
		"1259441-3106475",
	})
}

func TestFC2HUB_SearchMovie(t *testing.T) {
	testkit.Test(t, New, []string{
		"FC2-PPV-2725031",
		"fc2-2417378",
	})
}

func TestFC2HUBSetConfigSendsCookieAndUserAgent(t *testing.T) {
	var cookie, userAgent string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie = r.Header.Get("Cookie")
		userAgent = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<html><body>ok</body></html>"))
	}))
	defer server.Close()

	fc2hub := New()
	// No config yet: the clearance cookie must not be sent.
	require.NoError(t, fc2hub.collector().Visit(server.URL))
	assert.NotContains(t, cookie, "cf_clearance")

	config := envconfig.NewConfig()
	config.Set("cookie", "cf_clearance=abc123")
	config.Set("user_agent", "UA-TEST/157")
	require.NoError(t, fc2hub.SetConfig(config))
	require.NoError(t, fc2hub.collector().Visit(server.URL))
	assert.Contains(t, cookie, "cf_clearance=abc123")
	assert.Equal(t, "UA-TEST/157", userAgent)
}

func TestFC2HUBSetConfigAcceptsHyphenatedUserAgent(t *testing.T) {
	fc2hub := New()
	config := envconfig.NewConfig()
	config.Set("user-agent", "UA-HYPHEN/1")
	require.NoError(t, fc2hub.SetConfig(config))
	assert.Equal(t, "UA-HYPHEN/1", fc2hub.userAgent.Load())
}
