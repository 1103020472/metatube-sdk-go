// Package actorname 处理演员名列表中的「占位值」。
//
// 各站点在「这部片没有演员」时并不总是留空，JavDB 就常常直接给一个 "N/A"，
// 如果原样写进 MovieInfo.Actors，Emby 里就会出现一个叫 N/A 的演员。
package actorname

import "strings"

// placeholders 是各站点用来表示「没有演员」的占位值，比较时忽略大小写与首尾空格。
var placeholders = map[string]struct{}{
	"N/A": {},
	"N／A": {}, // 全角斜杠
	"NA":  {},
	"未知":  {},
	"不詳":  {},
	"-":   {},
}

// IsPlaceholder 判断演员名是否为「没有演员」的占位值。
func IsPlaceholder(name string) bool {
	_, ok := placeholders[strings.ToUpper(strings.TrimSpace(name))]
	return ok
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
