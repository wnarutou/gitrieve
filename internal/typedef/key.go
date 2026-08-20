package typedef

import (
	"strings"
)

// NormalizeURL 归一化仓库 URL，产出无协议、小写、无 www、无尾斜杠、无 .git 的
// 规范形态 "host/owner/repo"。处理顺序：去空白 → 去 #fragment → 小写 →
// 去 http(s):// → 去 www. → 去尾斜杠 → 去 .git。返回 "" 表示没有可用 URL。
func NormalizeURL(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if i := strings.IndexByte(s, '#'); i >= 0 {
		s = s[:i]
	}
	s = strings.ToLower(s)
	s = strings.TrimPrefix(s, "http://")
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "www.")
	s = strings.TrimSuffix(s, "/")
	s = strings.TrimSuffix(s, ".git")
	return s
}

// EffectiveURL 返回条目的有效 URL：URL 非空直接用；type 为 user/org 且 URL 为
// 空、orgName 非空时合成 "https://github.com/<orgName>"；否则返回 r.URL（可能
// 为空，即非法）。
func (r *Repository) EffectiveURL() string {
	if (r.GetType() == TypeUser || r.GetType() == TypeOrg) &&
		strings.TrimSpace(r.URL) == "" && r.OrgName != "" {
		return "https://github.com/" + r.OrgName
	}
	return r.URL
}

// Key 返回仓库身份键 = NormalizeURL(EffectiveURL())。空 URL 会得到 ""，
// 调用方（创建/更新/启动校验）必须拒绝。
func (r *Repository) Key() string {
	return NormalizeURL(r.EffectiveURL())
}

// Matches 判断一段用户输入是否命中该仓库：输入被规范化后与 Key() 比较。
// Key() 为空（无身份）或输入为空时返回 false。
func (r *Repository) Matches(input string) bool {
	key := r.Key()
	if key == "" {
		return false
	}
	return NormalizeURL(input) == key
}
