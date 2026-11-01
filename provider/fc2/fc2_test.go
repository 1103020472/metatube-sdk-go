package fc2

import (
	"strings"
	"testing"

	"github.com/antchfx/htmlquery"

	"github.com/1103020472/metatube-sdk-go/model"
	"github.com/1103020472/metatube-sdk-go/provider/internal/testkit"
)

func TestFC2_GetMovieInfoByID(t *testing.T) {
	testkit.Test(t, New, []string{
		"2812904",
		"4669533",
		"4815435",
	}, func(t *testing.T, a any) {
		info := a.(*model.MovieInfo)
		for _, unrelated := range []string{"【関連作品】", "カートに入れる", "マイリストに追加"} {
			if strings.Contains(info.Summary, unrelated) {
				t.Errorf("summary contains related-works content %q", unrelated)
			}
		}
	})
}

func TestExtractSummaryDropsRelatedWorks(t *testing.T) {
	const description = `<html><body><div>
		<h3>Current movie</h3>
		<p>Current movie description.</p>
		<script>window["token"]="not part of the summary";</script>
		<b>【関連作品】</b><br>
		<div><section class="recommend_list-100">Unrelated movie</section></div>
		<br>Unrelated tags
	</div></body></html>`

	doc, err := htmlquery.Parse(strings.NewReader(description))
	if err != nil {
		t.Fatal(err)
	}
	root := htmlquery.FindOne(doc, `//html/body/div`)
	if root == nil {
		t.Fatal("description wrapper not found")
	}

	summary := extractSummary(root)
	if !strings.Contains(summary, "Current movie description.") {
		t.Fatalf("current description missing from summary: %q", summary)
	}
	for _, unrelated := range []string{"【関連作品】", "Unrelated movie", "Unrelated tags", "not part of the summary"} {
		if strings.Contains(summary, unrelated) {
			t.Fatalf("summary contains unrelated content %q: %q", unrelated, summary)
		}
	}
}
