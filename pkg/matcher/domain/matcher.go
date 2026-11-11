/*
 * Copyright (C) 2020-2022, IrineSistiana
 *
 * This file is part of mosdns.
 *
 * mosdns is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * mosdns is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program.  If not, see <https://www.gnu.org/licenses/>.
 */

package domain

import (
	"errors"
	"fmt"
	"regexp"
	"regexp/syntax"
	"strings"
	"unicode/utf8"

	"github.com/IrineSistiana/mosdns/v5/pkg/utils"
)

var _ WriteableMatcher[any] = (*MixMatcher[any])(nil)
var _ WriteableMatcher[any] = (*SubDomainMatcher[any])(nil)
var _ WriteableMatcher[any] = (*FullMatcher[any])(nil)
var _ WriteableMatcher[any] = (*KeywordMatcher[any])(nil)
var _ WriteableMatcher[any] = (*RegexMatcher[any])(nil)

// 性能辅助函数：检查域名是否已经处于规范化状态（全小写，且末尾没有点），如果是，则无需分配新内存。
func tryFastNormalize(s string) string {
	if len(s) == 0 {
		return s
	}
	needsChange := false
	for i := 0; i < len(s); i++ {
		if (s[i] >= 'A' && s[i] <= 'Z') || (i == len(s)-1 && s[i] == '.') {
			needsChange = true
			break
		}
	}
	if !needsChange {
		return s // 直接保留原始大小写引用，实现零分配
	}
	return NormalizeDomain(s)
}

// NormalizeForMatch prepares a query name once for callers that need to run
// several matcher types. MatchNormalized methods below intentionally skip this
// check so a mixed matcher does not scan the same name four times.
func NormalizeForMatch(s string) string { return tryFastNormalize(s) }

// 内部高性能匹配接口
type internalMatcher[T any] interface {
	matchNormalized(s string) (T, bool)
}

type SubDomainMatcher[T any] struct {
	root *labelNode[T]
}

func NewSubDomainMatcher[T any]() *SubDomainMatcher[T] {
	return &SubDomainMatcher[T]{root: new(labelNode[T])}
}

func (m *SubDomainMatcher[T]) Match(s string) (T, bool) {
	return m.matchNormalized(tryFastNormalize(s))
}

func (m *SubDomainMatcher[T]) MatchNormalized(s string) (T, bool) {
	return m.matchNormalized(s)
}

func (m *SubDomainMatcher[T]) matchNormalized(s string) (T, bool) {
	s = TrimDot(s)
	currentNode := m.root
	v, ok := currentNode.getValue()
	for end := len(s); end > 0; {
		start := strings.LastIndexByte(s[:end], '.') + 1
		label := s[start:end]
		if nextNode := currentNode.getChild(label); nextNode != nil {
			if nextNode.hasValue() {
				v, ok = nextNode.getValue()
			}
			currentNode = nextNode
		} else {
			break
		}
		if start == 0 {
			break
		}
		end = start - 1
	}
	return v, ok
}

func (m *SubDomainMatcher[T]) Len() int {
	return m.root.len()
}

func (m *SubDomainMatcher[T]) Add(s string, v T) error {
	s = NormalizeDomain(s)
	ds := NewReverseDomainScanner(s)
	currentNode := m.root
	for ds.Scan() {
		label := ds.NextLabel()
		if child := currentNode.getChild(label); child != nil {
			currentNode = child
		} else {
			currentNode = currentNode.newChild(label)
		}
	}
	currentNode.storeValue(v)
	return nil
}

type FullMatcher[T any] struct {
	m map[string]T // string in is map must be a normalized domain (See NormalizeDomain).
}

func NewFullMatcher[T any]() *FullMatcher[T] {
	return &FullMatcher[T]{
		m: make(map[string]T),
	}
}

// Add adds domain s to this matcher, s can be a fqdn or not.
func (m *FullMatcher[T]) Add(s string, v T) error {
	s = NormalizeDomain(s)
	if _, ok := m.m[s]; !ok {
		s = strings.Clone(s)
	}
	m.m[s] = v
	return nil
}

func (m *FullMatcher[T]) Match(s string) (v T, ok bool) {
	return m.matchNormalized(tryFastNormalize(s))
}

func (m *FullMatcher[T]) MatchNormalized(s string) (v T, ok bool) {
	return m.matchNormalized(s)
}

func (m *FullMatcher[T]) matchNormalized(s string) (v T, ok bool) {
	v, ok = m.m[s]
	return
}

func (m *FullMatcher[T]) Len() int {
	return len(m.m)
}

type KeywordMatcher[T any] struct {
	kws map[string]T
}

func NewKeywordMatcher[T any]() *KeywordMatcher[T] {
	return &KeywordMatcher[T]{
		kws: make(map[string]T),
	}
}

func (m *KeywordMatcher[T]) Add(keyword string, v T) error {
	keyword = NormalizeDomain(keyword) // fqdn-insensitive and case-insensitive
	if _, ok := m.kws[keyword]; !ok {
		keyword = strings.Clone(keyword)
	}
	m.kws[keyword] = v
	return nil
}

func (m *KeywordMatcher[T]) Match(s string) (v T, ok bool) {
	return m.matchNormalized(tryFastNormalize(s))
}

func (m *KeywordMatcher[T]) MatchNormalized(s string) (v T, ok bool) {
	return m.matchNormalized(s)
}

func (m *KeywordMatcher[T]) matchNormalized(s string) (v T, ok bool) {
	for k, v := range m.kws {
		if strings.Contains(s, k) {
			return v, true
		}
	}
	return v, false
}

func (m *KeywordMatcher[T]) Len() int {
	return len(m.kws)
}

// RegexMatcher contains regexp rules.
// Note: the regexp rule is expect to match a lower-case non fqdn.
type RegexMatcher[T any] struct {
	// byExpr keeps Add idempotent while regs avoids map iteration and hash
	// overhead on every query. Rules are built during configuration loading
	// and then matched concurrently without mutation.
	byExpr map[string]*regElem[T]
	regs   []*regElem[T]
}

type regElem[T any] struct {
	reg    *regexp.Regexp
	v      T
	suffix string
	hint   string
	// fast is an allocation-free equivalent for the small set of regular
	// expressions emitted by the built-in DNS rule lists. Other expressions
	// continue to use the regexp engine as the source of truth.
	fast func(string) bool
}

func NewRegexMatcher[T any]() *RegexMatcher[T] {
	return &RegexMatcher[T]{byExpr: make(map[string]*regElem[T]), regs: make([]*regElem[T], 0)}
}

func (m *RegexMatcher[T]) Add(expr string, v T) error {
	e := m.byExpr[expr]
	if e == nil {
		expr = strings.Clone(expr)
		reg, err := regexp.Compile(expr)
		if err != nil {
			return err
		}
		e = &regElem[T]{
			reg:    reg,
			v:      v,
			suffix: requiredRegexSuffix(expr),
			hint:   requiredRegexLiteral(reg, expr),
			fast:   fastRegexpMatcher(expr),
		}
		m.byExpr[expr] = e
		m.regs = append(m.regs, e)
	} else {
		e.v = v
	}
	return nil
}

func (m *RegexMatcher[T]) Match(s string) (v T, ok bool) {
	return m.matchNormalized(tryFastNormalize(s))
}

func (m *RegexMatcher[T]) MatchNormalized(s string) (v T, ok bool) {
	return m.matchNormalized(s)
}

func (m *RegexMatcher[T]) matchNormalized(s string) (v T, ok bool) {
	for _, e := range m.regs {
		if e.suffix != "" && !strings.HasSuffix(s, e.suffix) {
			continue
		}
		if e.hint != "" && !strings.Contains(s, e.hint) {
			continue
		}
		if e.fast != nil {
			if e.fast(s) {
				return e.v, true
			}
			continue
		}
		if e.reg.MatchString(s) {
			return e.v, true
		}
	}
	var zeroT T
	return zeroT, false
}

func (m *RegexMatcher[T]) Len() int {
	return len(m.byExpr)
}

// requiredRegexLiteral extracts a conservative required substring. A literal
// prefix reported by regexp is always required; for the common (^|\.) domain
// boundary form we also accept the longest plain run when the expression has
// no alternation or grouping that could make it optional.
func requiredRegexLiteral(reg *regexp.Regexp, expr string) string {
	if prefix, _ := reg.LiteralPrefix(); len(prefix) >= 3 {
		return prefix
	}
	parsed, err := syntax.Parse(expr, syntax.Perl)
	if err != nil {
		return ""
	}
	best := requiredSyntaxLiteral(parsed)
	if len(best) < 3 {
		return ""
	}
	return best
}

// requiredSyntaxLiteral returns a literal substring present in every string
// accepted by the syntax node. It is deliberately conservative: optional
// branches and alternations without a common literal produce no hint. A hint
// is only used to reject a rule early, so a false negative is safe while a
// false positive would change matching behavior.
func requiredSyntaxLiteral(r *syntax.Regexp) string {
	if r == nil {
		return ""
	}
	switch r.Op {
	case syntax.OpLiteral:
		if r.Flags&syntax.FoldCase != 0 || len(r.Rune) == 0 {
			return ""
		}
		return string(r.Rune)
	case syntax.OpCapture:
		if len(r.Sub) == 1 {
			return requiredSyntaxLiteral(r.Sub[0])
		}
	case syntax.OpConcat:
		best := ""
		for _, sub := range r.Sub {
			if candidate := requiredSyntaxLiteral(sub); len(candidate) > len(best) {
				best = candidate
			}
		}
		return best
	case syntax.OpAlternate:
		if len(r.Sub) == 0 {
			return ""
		}
		common := requiredSyntaxLiteral(r.Sub[0])
		for _, sub := range r.Sub[1:] {
			common = longestCommonSubstring(common, requiredSyntaxLiteral(sub))
			if common == "" {
				return ""
			}
		}
		return common
	case syntax.OpPlus:
		if len(r.Sub) == 1 {
			return requiredSyntaxLiteral(r.Sub[0])
		}
	case syntax.OpRepeat:
		if r.Min > 0 && len(r.Sub) == 1 {
			return requiredSyntaxLiteral(r.Sub[0])
		}
	}
	return ""
}

func longestCommonSubstring(a, b string) string {
	if len(a) == 0 || len(b) == 0 {
		return ""
	}
	if len(a) > len(b) {
		a, b = b, a
	}
	best := ""
	for start := 0; start < len(a); start++ {
		for end := len(a); end > start+len(best); end-- {
			candidate := a[start:end]
			if strings.Contains(b, candidate) {
				best = candidate
				break
			}
		}
	}
	return best
}

// requiredRegexSuffix extracts a literal suffix from an expression anchored
// at the end. The conservative parser only accepts a final escaped dot plus
// plain label characters, so a false negative falls back to regexp matching.
func requiredRegexSuffix(expr string) string {
	if !strings.HasSuffix(expr, "$") {
		return ""
	}
	body := strings.TrimSuffix(expr, "$")
	index := strings.LastIndex(body, `\.`)
	if index < 0 || index+2 >= len(body) {
		return ""
	}
	for i := index + 2; i < len(body); i++ {
		c := body[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '_' || c == '-' || c == '.' {
			continue
		}
		return ""
	}
	return "." + body[index+2:]
}

// fastRegexpMatcher returns only byte-for-byte equivalent shortcuts for
// expressions shipped in the built-in rule files.
func fastRegexpMatcher(expr string) func(string) bool {
	switch expr {
	case `[^a-zA-Z0-9._-]`:
		return func(s string) bool {
			for i := 0; i < len(s); i++ {
				c := s[i]
				if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
					(c >= '0' && c <= '9') || c == '.' || c == '_' || c == '-' {
					continue
				}
				return true
			}
			return false
		}
	case `^[^.]+$`:
		return func(s string) bool {
			return len(s) > 0 && !strings.Contains(s, ".")
		}
	case `^.*.lan$`:
		return func(s string) bool { return len(s) >= 4 && strings.HasSuffix(s, "lan") }
	case `^.*.arpa$`:
		return func(s string) bool { return len(s) >= 5 && strings.HasSuffix(s, "arpa") }
	case `^time[1-5]?\..*\.com$`:
		return func(s string) bool {
			if !strings.HasPrefix(s, "time") {
				return false
			}
			i := len("time")
			if i < len(s) && s[i] >= '1' && s[i] <= '5' {
				i++
			}
			return i < len(s) && s[i] == '.' && len(s)-i-1 >= len(".com") && strings.HasSuffix(s, ".com")
		}
	case `^ntp[1-2]?\..*\.com$`:
		return func(s string) bool {
			if !strings.HasPrefix(s, "ntp") {
				return false
			}
			i := len("ntp")
			if i < len(s) && s[i] >= '1' && s[i] <= '2' {
				i++
			}
			return i < len(s) && s[i] == '.' && len(s)-i-1 >= len(".com") && strings.HasSuffix(s, ".com")
		}
	case `^v[0-9]+-dy-.*\.zjcdn\.com$`:
		return func(s string) bool {
			if !strings.HasPrefix(s, "v") {
				return false
			}
			i := 1
			for i < len(s) && s[i] >= '0' && s[i] <= '9' {
				i++
			}
			if i == 1 || !strings.HasPrefix(s[i:], "-dy-") {
				return false
			}
			return len(s)-i-4 >= len(".zjcdn.com") && strings.HasSuffix(s, ".zjcdn.com")
		}
	case `.+\.awsdns-[0-9][0-9]\.(co\.uk|com|net|org)$`:
		return func(s string) bool {
			const prefix = ".awsdns-"
			for start := 0; ; {
				i := strings.Index(s[start:], prefix)
				if i < 0 {
					return false
				}
				i += start
				if i > 0 && i+len(prefix)+2 < len(s) &&
					s[i+len(prefix)] >= '0' && s[i+len(prefix)] <= '9' &&
					s[i+len(prefix)+1] >= '0' && s[i+len(prefix)+1] <= '9' &&
					hasOneOfSuffix(s[i+len(prefix)+2:], ".co.uk", ".com", ".net", ".org") {
					return true
				}
				start = i + 1
			}
		}
	case `.+\.dkr\.ecr\.[^\.]+\.amazonaws\.com$`:
		return func(s string) bool {
			const prefix = ".dkr.ecr."
			const suffix = ".amazonaws.com"
			for start := 0; ; {
				i := strings.Index(s[start:], prefix)
				if i < 0 {
					return false
				}
				i += start
				middleStart := i + len(prefix)
				if i > 0 {
					if end := len(s) - len(suffix); end > middleStart && strings.HasSuffix(s, suffix) && !strings.Contains(s[middleStart:end], ".") {
						return true
					}
				}
				start = i + 1
			}
		}
	case `^[0-9]+vod-adaptive\.akamaized\.net$`:
		return func(s string) bool {
			const suffix = "vod-adaptive.akamaized.net"
			i := len(s) - len(suffix)
			if i <= 0 || !strings.HasSuffix(s, suffix) {
				return false
			}
			return allASCIIDigits(s[:i])
		}
	case `^chatgpt-async-webps-prod-\S+-\d+\.webpubsub\.azure\.com$`:
		return func(s string) bool {
			const prefix = "chatgpt-async-webps-prod-"
			const suffix = ".webpubsub.azure.com"
			if !strings.HasPrefix(s, prefix) || !strings.HasSuffix(s, suffix) {
				return false
			}
			body := s[len(prefix) : len(s)-len(suffix)]
			i := strings.LastIndexByte(body, '-')
			return i > 0 && i+1 < len(body) && allNonSpace(body[:i]) && allASCIIDigits(body[i+1:])
		}
	case `^epicgames-download\d\.akamaized\.net$`:
		return func(s string) bool {
			const prefix = "epicgames-download"
			const suffix = ".akamaized.net"
			return len(s) == len(prefix)+1+len(suffix) && strings.HasPrefix(s, prefix) &&
				s[len(prefix)] >= '0' && s[len(prefix)] <= '9' && strings.HasSuffix(s, suffix)
		}
	case `^ewcdn[0-9]+\.nowe\.com$`:
		return func(s string) bool {
			const prefix = "ewcdn"
			const suffix = ".nowe.com"
			if !strings.HasPrefix(s, prefix) || !strings.HasSuffix(s, suffix) {
				return false
			}
			return allASCIIDigits(s[len(prefix) : len(s)-len(suffix)])
		}
	case `^github-production-release-asset-[0-9a-zA-Z]{6}\.s3\.amazonaws\.com$`:
		return func(s string) bool {
			const prefix = "github-production-release-asset-"
			const suffix = ".s3.amazonaws.com"
			if len(s) != len(prefix)+6+len(suffix) || !strings.HasPrefix(s, prefix) || !strings.HasSuffix(s, suffix) {
				return false
			}
			return allASCIIAlphaNumeric(s[len(prefix) : len(s)-len(suffix)])
		}
	case `^hses[1-7]?\.akamaized\.net$`:
		return func(s string) bool {
			const prefix = "hses"
			const suffix = ".akamaized.net"
			if !strings.HasPrefix(s, prefix) || !strings.HasSuffix(s, suffix) {
				return false
			}
			body := s[len(prefix) : len(s)-len(suffix)]
			return body == "" || len(body) == 1 && body[0] >= '1' && body[0] <= '7'
		}
	case `javdb\d+\.com$`:
		return func(s string) bool {
			const prefix = "javdb"
			const suffix = ".com"
			for start := 0; ; {
				i := strings.Index(s[start:], prefix)
				if i < 0 {
					return false
				}
				i += start
				middleStart := i + len(prefix)
				if middleStart < len(s) && strings.HasSuffix(s, suffix) && allASCIIDigits(s[middleStart:len(s)-len(suffix)]) {
					return true
				}
				start = i + 1
			}
		}
	default:
		return compileBoundaryMatcher(expr)
	}
}

func allASCIIDigits(s string) bool {
	if len(s) == 0 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func allASCIIAlphaNumeric(s string) bool {
	if len(s) == 0 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')) {
			return false
		}
	}
	return true
}

func allNonSpace(s string) bool {
	if len(s) == 0 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] == ' ' || s[i] == '\t' || s[i] == '\r' || s[i] == '\n' {
			return false
		}
	}
	return true
}

func hasOneOfSuffix(s string, suffixes ...string) bool {
	for _, suffix := range suffixes {
		if strings.HasSuffix(s, suffix) {
			return true
		}
	}
	return false
}

// boundaryMatcher is a small, allocation-free matcher for the regular
// expressions used by the geolocation rule list. Those expressions all have
// the form (^|\.)<pattern>$: the pattern is tested at the start of the name or
// immediately after a dot and must consume the rest of the name. Compiling
// the limited regexp syntax into atoms avoids invoking regexp's general
// machine for every query while unsupported expressions retain the regexp
// implementation as the source of truth.
type boundaryMatcher struct {
	variants [][]boundaryAtom
}

type boundaryAtom struct {
	literal string
	runes   []rune
	any     bool
	min     int
	max     int // -1 means unbounded.
}

func compileBoundaryMatcher(expr string) func(string) bool {
	const prefix = `(^|\.)`
	if !strings.HasPrefix(expr, prefix) || !strings.HasSuffix(expr, "$") {
		return nil
	}
	body := strings.TrimSuffix(expr[len(prefix):], "$")
	parsed, err := syntax.Parse(body, syntax.Perl)
	if err != nil {
		return nil
	}
	variants, ok := compileBoundaryVariants(parsed)
	if !ok || len(variants) == 0 || len(variants) > 256 {
		return nil
	}
	m := &boundaryMatcher{variants: variants}
	return m.match
}

func compileBoundaryVariants(r *syntax.Regexp) ([][]boundaryAtom, bool) {
	switch r.Op {
	case syntax.OpEmptyMatch:
		return [][]boundaryAtom{{}}, true
	case syntax.OpBeginText, syntax.OpEndText, syntax.OpBeginLine, syntax.OpEndLine:
		// The outer expression already owns the end-of-name anchor. Anchors
		// appearing inside the body are deliberately left to regexp so this
		// shortcut cannot silently broaden an unusual user expression.
		return nil, false
	case syntax.OpCapture:
		if len(r.Sub) != 1 {
			return nil, false
		}
		return compileBoundaryVariants(r.Sub[0])
	case syntax.OpLiteral:
		if r.Flags&syntax.FoldCase != 0 || len(r.Rune) == 0 {
			return nil, false
		}
		return [][]boundaryAtom{{{literal: string(r.Rune), min: 1, max: 1}}}, true
	case syntax.OpCharClass:
		if len(r.Rune) == 0 {
			return nil, false
		}
		return [][]boundaryAtom{{{runes: append([]rune(nil), r.Rune...), min: 1, max: 1}}}, true
	case syntax.OpAnyCharNotNL, syntax.OpAnyChar:
		return [][]boundaryAtom{{{any: true, min: 1, max: 1}}}, true
	case syntax.OpConcat:
		variants := [][]boundaryAtom{{}}
		for _, sub := range r.Sub {
			subVariants, ok := compileBoundaryVariants(sub)
			if !ok || len(variants)*len(subVariants) > 256 {
				return nil, false
			}
			next := make([][]boundaryAtom, 0, len(variants)*len(subVariants))
			for _, left := range variants {
				for _, right := range subVariants {
					merged := make([]boundaryAtom, 0, len(left)+len(right))
					merged = append(merged, left...)
					merged = append(merged, right...)
					next = append(next, merged)
				}
			}
			variants = next
		}
		return variants, true
	case syntax.OpAlternate:
		variants := make([][]boundaryAtom, 0, len(r.Sub))
		for _, sub := range r.Sub {
			subVariants, ok := compileBoundaryVariants(sub)
			if !ok || len(variants)+len(subVariants) > 256 {
				return nil, false
			}
			variants = append(variants, subVariants...)
		}
		return variants, true
	case syntax.OpQuest:
		if len(r.Sub) != 1 {
			return nil, false
		}
		variants, ok := compileBoundaryVariants(r.Sub[0])
		if !ok || len(variants)+1 > 256 {
			return nil, false
		}
		return append([][]boundaryAtom{{}}, variants...), true
	case syntax.OpStar, syntax.OpPlus, syntax.OpRepeat:
		if len(r.Sub) != 1 {
			return nil, false
		}
		atom, ok := compileBoundaryAtom(r.Sub[0])
		if !ok {
			return nil, false
		}
		switch r.Op {
		case syntax.OpQuest:
			atom.min, atom.max = 0, 1
		case syntax.OpStar:
			atom.min, atom.max = 0, -1
		case syntax.OpPlus:
			atom.min, atom.max = 1, -1
		case syntax.OpRepeat:
			atom.min, atom.max = r.Min, r.Max
		}
		return [][]boundaryAtom{{atom}}, true
	default:
		return nil, false
	}
}

func compileBoundaryAtom(r *syntax.Regexp) (boundaryAtom, bool) {
	if r.Op == syntax.OpCapture {
		if len(r.Sub) != 1 {
			return boundaryAtom{}, false
		}
		return compileBoundaryAtom(r.Sub[0])
	}
	switch r.Op {
	case syntax.OpLiteral:
		if r.Flags&syntax.FoldCase != 0 || len(r.Rune) == 0 {
			return boundaryAtom{}, false
		}
		return boundaryAtom{literal: string(r.Rune), min: 1, max: 1}, true
	case syntax.OpCharClass:
		if len(r.Rune) == 0 {
			return boundaryAtom{}, false
		}
		return boundaryAtom{runes: append([]rune(nil), r.Rune...), min: 1, max: 1}, true
	case syntax.OpAnyCharNotNL, syntax.OpAnyChar:
		return boundaryAtom{any: true, min: 1, max: 1}, true
	default:
		return boundaryAtom{}, false
	}
}

func (m *boundaryMatcher) match(s string) bool {
	for start := 0; ; {
		for _, variant := range m.variants {
			if matchBoundaryVariant(variant, s, start, 0) {
				return true
			}
		}
		index := strings.IndexByte(s[start:], '.')
		if index < 0 {
			return false
		}
		start += index + 1
	}
}

func matchBoundaryVariant(variant []boundaryAtom, s string, pos, index int) bool {
	if index == len(variant) {
		return pos == len(s)
	}
	atom := variant[index]
	max := atom.max
	if max < 0 || max > len(s)-pos {
		max = len(s) - pos
	}
	count := 0
	end := pos
	for count < max {
		next, ok := matchBoundaryAtom(atom, s, end)
		if !ok || next <= end {
			break
		}
		end = next
		count++
	}
	if count < atom.min {
		return false
	}
	for n := count; n >= atom.min; n-- {
		if matchBoundaryVariant(variant, s, posForBoundaryCount(atom, s, pos, n), index+1) {
			return true
		}
		if n > atom.min {
			end = posForBoundaryCount(atom, s, pos, n-1)
		}
	}
	return false
}

func posForBoundaryCount(atom boundaryAtom, s string, pos, count int) int {
	for i := 0; i < count; i++ {
		next, ok := matchBoundaryAtom(atom, s, pos)
		if !ok || next <= pos {
			return pos
		}
		pos = next
	}
	return pos
}

func matchBoundaryAtom(atom boundaryAtom, s string, pos int) (int, bool) {
	if atom.literal != "" {
		if strings.HasPrefix(s[pos:], atom.literal) {
			return pos + len(atom.literal), true
		}
		return pos, false
	}
	if pos >= len(s) {
		return pos, false
	}
	if atom.any {
		r, size := utf8.DecodeRuneInString(s[pos:])
		return pos + size, r != '\n'
	}
	c, size := utf8.DecodeRuneInString(s[pos:])
	for i := 0; i+1 < len(atom.runes); i += 2 {
		if c >= atom.runes[i] && c <= atom.runes[i+1] {
			return pos + size, true
		}
	}
	return pos, false
}

const (
	MatcherFull    = "full"
	MatcherDomain  = "domain"
	MatcherRegexp  = "regexp"
	MatcherKeyword = "keyword"
)

const (
	matcherHasFull uint8 = 1 << iota
	matcherHasDomain
	matcherHasRegexp
	matcherHasKeyword
)

type MixMatcher[T any] struct {
	defaultMatcher string

	full    *FullMatcher[T]
	domain  *SubDomainMatcher[T]
	regex   *RegexMatcher[T]
	keyword *KeywordMatcher[T]
	present uint8
}

func NewMixMatcher[T any]() *MixMatcher[T] {
	return &MixMatcher[T]{
		full:    NewFullMatcher[T](),
		domain:  NewSubDomainMatcher[T](),
		regex:   NewRegexMatcher[T](),
		keyword: NewKeywordMatcher[T](),
	}
}

func (m *MixMatcher[T]) SetDefaultMatcher(s string) {
	m.defaultMatcher = s
}

func (m *MixMatcher[T]) getSubMatcher(typ string) (WriteableMatcher[T], uint8) {
	switch typ {
	case MatcherFull:
		return m.full, matcherHasFull
	case MatcherDomain:
		return m.domain, matcherHasDomain
	case MatcherRegexp:
		return m.regex, matcherHasRegexp
	case MatcherKeyword:
		return m.keyword, matcherHasKeyword
	}
	return nil, 0
}

func (m *MixMatcher[T]) GetSubMatcher(typ string) WriteableMatcher[T] {
	sm, present := m.getSubMatcher(typ)
	if sm != nil {
		// The caller may add directly through the returned interface. Mark the
		// matcher type here so Match cannot skip that rule set.
		m.present |= present
	}
	return sm
}

var ErrNodefaultMatcher = errors.New("default matcher is not set")

func (m *MixMatcher[T]) Add(s string, v T) error {
	typ, pattern := m.splitTypeAndPattern(s)
	if len(typ) == 0 {
		if len(m.defaultMatcher) != 0 {
			typ = m.defaultMatcher
		} else {
			return ErrNodefaultMatcher
		}
	}
	sm, present := m.getSubMatcher(typ)
	if sm == nil {
		return fmt.Errorf("unsupported match type [%s]", typ)
	}
	if err := sm.Add(pattern, v); err != nil {
		return err
	}
	m.present |= present
	return nil
}

func (m *MixMatcher[T]) Match(s string) (v T, ok bool) {
	switch m.present {
	case 0:
		return v, false
	case matcherHasDomain:
		return m.domain.matchNormalized(tryFastNormalize(s))
	}
	// 核心性能点：在此处统一计算一次规范化。如果是已经是规范的（比如 google.com），
	// tryFastNormalize 会返回原字符串引用，实现零内存分配。
	s = tryFastNormalize(s)
	if m.present&matcherHasFull != 0 {
		if v, ok = m.full.matchNormalized(s); ok {
			return v, true
		}
	}
	if m.present&matcherHasDomain != 0 {
		if v, ok = m.domain.matchNormalized(s); ok {
			return v, true
		}
	}
	if m.present&matcherHasRegexp != 0 {
		if v, ok = m.regex.matchNormalized(s); ok {
			return v, true
		}
	}
	if m.present&matcherHasKeyword != 0 {
		if v, ok = m.keyword.matchNormalized(s); ok {
			return v, true
		}
	}
	return
}

func (m *MixMatcher[T]) Len() int {
	sum := 0
	for _, matcher := range [...]interface{ Len() int }{m.full, m.domain, m.regex, m.keyword} {
		if matcher == nil {
			continue
		}
		sum += matcher.Len()
	}
	return sum
}

func (m *MixMatcher[T]) splitTypeAndPattern(s string) (string, string) {
	typ, pattern, ok := utils.SplitString2(s, ":")
	if !ok {
		pattern = s
	}
	return typ, pattern
}
