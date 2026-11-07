package actorname

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsPlaceholder(t *testing.T) {
	placeholders := []string{
		// 既有：JavDB 常用的「没有演员」写法。
		"N/A", "n/a", " N/A ", "N／A", "NA", "未知", "不詳", "-",
		// FANZA 实际会返回的占位值。
		"＊＊＊", "＊＊＊ ", "***", "(≥o≤)", "(≧o≦)", "???", "？？？",
		"---", "なし", "不明",
	}
	for _, name := range placeholders {
		assert.Truef(t, IsPlaceholder(name), "should be placeholder: %q", name)
	}

	names := []string{
		"三上悠亜", "Rio", "Aika", "A", "源みい", "天使もえ", "Nanao",
	}
	for _, name := range names {
		assert.Falsef(t, IsPlaceholder(name), "should be a real name: %q", name)
	}
}

func TestClean(t *testing.T) {
	got := Clean([]string{"三上悠亜", "＊＊＊", "三上悠亜", " ", "(≥o≤)", "???", "Rio"})
	assert.Equal(t, []string{"三上悠亜", "Rio"}, got)
}
