// Package actorname 处理演员名列表中的「占位值」。
//
// 各站点在「这部片没有演员」时并不总是留空，JavDB 就常常直接给一个 "N/A"，
// 如果原样写进 MovieInfo.Actors，Emby 里就会出现一个叫 N/A 的演员。
package actorname

import (
	"strings"
	"unicode"
)

// placeholders 是各站点用来表示「没有演员」的占位值。
// key 是 normalize 之后的形式（全角折半角、去掉所有空白、转小写）。
var placeholders = map[string]struct{}{
	"n/a":  {},
	"na":   {},
	"未知":   {},
	"不詳":   {},
	"不明":   {},
	"非公開":  {},
	"匿名":   {},
	"なし":   {},
	"無し":   {},
	"-":    {},
	"--":   {},
	"---":  {},
	"***":  {},
	"???":  {},
}

// IsPlaceholder 判断演员名是否为「没有演员」的占位值。
func IsPlaceholder(name string) bool {
	s := normalize(name)
	if s == "" {
		return false
	}
	if _, ok := placeholders[s]; ok {
		return true
	}

	// 兜底规则：整串只有标点/符号（＊＊＊、???、---），
	// 或「符号 + 至多一个拉丁字母」的颜文字（(≥o≤)、(≧o≦)）。
	// 这类值里没有任何真正的名字成分，站点用它们表示「没有演员」。
	var letters int
	var hasSymbol bool
	for _, r := range s {
		switch {
		case unicode.IsLetter(r):
			letters++
		case unicode.IsSymbol(r), unicode.IsPunct(r):
			hasSymbol = true
		}
	}
	return hasSymbol && letters <= 1
}

// normalize 把演员名规整成便于比较的形式：
// 全角 ASCII 折成半角、去掉所有空白、转小写。
// 例如「＊＊＊」→「***」、「？？？」→「???」、「N／A」→「n/a」。
func normalize(name string) string {
	var b strings.Builder
	b.Grow(len(name))
	for _, r := range name {
		if unicode.IsSpace(r) {
			continue
		}
		if r >= '\uFF01' && r <= '\uFF5E' { // 全角 ASCII（！-～）
			r -= 0xFEE0
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// Clean 去掉空值与占位值，并按原有顺序去重。
func Clean(names []string) []string {
	if len(names) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(names))
	cleaned := make([]string, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" || IsPlaceholder(name) {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		cleaned = append(cleaned, name)
	}
	return cleaned
}
