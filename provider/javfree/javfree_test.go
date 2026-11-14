package javfree

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/1103020472/metatube-sdk-go/provider/internal/testkit"
)

func TestJAVFREE_GetMovieInfoByID(t *testing.T) {
	testkit.Test(t, New, []string{
		"243452-1151912",
		"402171-4608186",
	})
}

func TestJAVFREE_SearchMovie(t *testing.T) {
	testkit.Test(t, New, []string{
		"FC2-PPV-1151912",
	})
}

func TestJAVFREE_FC2Number(t *testing.T) {
	for _, unit := range []struct {
		in, want string
	}{
		{"4752128", "4752128"},
		{"FC2-4752128", "4752128"},
		{"FC2-PPV-4752128", "4752128"},
		{"FC2 4752128", "4752128"},
		{"FC2PPV4752128", "4752128"},
		{"4752128-C", "4752128"},
		{"", ""},
		{"unknown", ""},
	} {
		assert.Equal(t, unit.want, fc2Number(unit.in), unit.in)
	}
}

func TestJAVFREE_SearchURLUsesSearchPath(t *testing.T) {
	// /?s= rejects plain numbers with 403, /search/{kw}/ is what the site's
	// own search box uses.
	assert.Equal(t, "https://javfree.me/search/4752128/",
		fmt.Sprintf(searchURL, "4752128"))
}
