// Copyright 2013 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package language implements BCP 47 language tags and related functionality.
//
// The Tag type, which is used to represent language tags, is agnostic to the
// meaning of its subtags. Tags are not fully canonicalized to preserve
// information that may be valuable in certain contexts. As a consequence, two
// different tags may represent identical languages in certain contexts.
//
// To determine equivalence between tags, a user should typically use a Matcher
// that is aware of the intricacies of equivalence within the given context.
// The default Matcher implementation provided in this package takes into
// account things such as deprecated subtags, legacy tags, and mutual
// intelligibility between scripts and languages.
//
// See http://tools.ietf.org/html/bcp47 for more details.
//
// NOTE: This package is still under development. Parts of it are not yet
// implemented, and the API is subject to change.
package language

import (
	"errors"
	"fmt"
	"strings"
)

const (
	// maxCoreSize is the maximum size of a BCP 47 tag without variants and
	// extensions. Equals max lang (3) + script (4) + max reg (3) + 2 dashes.
	maxCoreSize = 12

	// max99thPercentileSize is a somewhat arbitrary buffer size that presumably
	// is large enough to hold at least 99% of the BCP 47 tags.
	max99thPercentileSize = 32

	// maxSimpleUExtensionSize is the maximum size of a -u extension with one
	// key-type pair. Equals len("-u-") + key (2) + dash + max value (8).
	maxSimpleUExtensionSize = 14
)

// Tag represents a BCP 47 language tag. It is used to specify an instance of a
// specific language or locale. All language tag values are guaranteed to be
// well-formed.
type Tag struct {
	lang     langID
	region   regionID
	script   scriptID
	pVariant byte   // offset in str, includes preceding '-'
	pExt     uint16 // offset of first extension, includes preceding '-'

	// str is the string representation of the Tag. It will only be used if the
	// tag has variants or extensions.
	str string
}

// Make is a convenience wrapper for Parse that omits the error.
// In case of an error, a sensible default is returned.
func Make(s string) Tag {
	return Default.Make(s)
}

// Make is a convenience wrapper for c.Parse that omits the error.
// In case of an error, a sensible default is returned.
func (c CanonType) Make(s string) Tag {
	t, _ := c.Parse(s)
	return t
}

// Raw returns the raw base language, script and region, without making an
// attempt to infer their values.
func (t Tag) Raw() (b Base, s Script, r Region) {
	return Base{t.lang}, Script{t.script}, Region{t.region}
}

// equalTags compares language, script and region subtags only.
func (t Tag) equalTags(a Tag) bool {
	return t.lang == a.lang && t.script == a.script && t.region == a.region
}

// IsRoot returns true if t is equal to language "und".
func (t Tag) IsRoot() bool {
	if int(t.pVariant) < len(t.str) {
		return false
	}
	return t.equalTags(und)
}

// private reports whether the Tag consists solely of a private use tag.
func (t Tag) private() bool {
	return t.str != "" && t.pVariant == 0
}

// CanonType can be used to enable or disable various types of canonicalization.
type CanonType int

const (
	// Replace deprecated base languages with their preferred replacements.
	DeprecatedBase CanonType = 1 << iota
	// Replace deprecated scripts with their preferred replacements.
	DeprecatedScript
	// Replace deprecated regions with their preferred replacements.
	DeprecatedRegion
	// Remove redundant scripts.
	SuppressScript
	// Normalize legacy encodings, as defined by CLDR.
	Legacy
	// Map the dominant language of a macro language group to the macro language
	// subtag. For example cmn -> zh.
	Macro
	// The CLDR flag should be used if full compatibility with CLDR is required.
	// There are a few cases where language.Tag may differ from CLDR. To follow all
	// of CLDR's suggestions, use All|CLDR.
	CLDR

	// Raw can be used to Compose or Parse without Canonicalization.
	Raw CanonType = 0

	// Replace all deprecated tags with their preferred replacements.
	Deprecated = DeprecatedBase | DeprecatedScript | DeprecatedRegion

	// All canonicalizations recommended by BCP 47.
	BCP47 = Deprecated | SuppressScript

	// All canonicalizations.
	All = BCP47 | Legacy | Macro

	// Default is the canonicalization used by Parse, Make and Compose. To
	// preserve as much information as possible, canonicalizations that remove
	// potentially valuable information are not included. The Matcher is
	// designed to recognize similar tags that would be the same if
	// they were canonicalized using All.
	Default = Deprecated | Legacy

	// TODO: LikelyScript, LikelyRegion: suppress similar to ICU.
)

// canonicalize returns the canonicalized equivalent of the tag and
// whether there was any change.
func (t Tag) canonicalize(c CanonType) (Tag, bool) {
	if c == Raw {
		return t, false
	}
	changed := false
	if c&SuppressScript != 0 {
		if t.lang < langNoIndexOffset && uint8(t.script) == suppressScript[t.lang] {
			t.script = 0
			changed = true
		}
	}
	if c&Legacy != 0 {
		// We hard code this set as it is very small, unlikely to change and requires some
		// handling that does not fit elsewhere.
		switch t.lang {
		case _no:
			if c&CLDR != 0 {
				t.lang = _nb
				changed = true
			}
		case _tl:
			t.lang = _fil
			changed = true
		case _sh:
			if t.script == 0 {
				t.script = _Latn
			}
			t.lang = _sr
			changed = true
		}
	}
	if c&DeprecatedBase != 0 {
		l := normLang(langOldMap[:], t.lang)
		if l != t.lang {
			// CLDR maps "mo" to "ro". This mapping loses the piece of information
			// that "mo" very likely implies the region "MD". This may be important
			// for applications that insist on making a difference between these
			// two language codes.
			if t.lang == _mo && t.region == 0 && c&CLDR == 0 {
				t.region = _MD
			}
			changed = true
			t.lang = l
		}
	}
	if c&DeprecatedScript != 0 {
		if t.script == _Qaai {
			changed = true
			t.script = _Zinh
		}
	}
	if c&DeprecatedRegion != 0 {
		if r := normRegion(t.region); r != 0 {
			changed = true
			t.region = r
		}
	}
	if c&Macro != 0 {
		// We deviate here from CLDR. The mapping "nb" -> "no" qualifies as a typical
		// Macro language mapping.  However, for legacy reasons, CLDR maps "no",
		// the macro language code for Norwegian, to the dominant variant "nb".
		// This change is currently under consideration for CLDR as well.
		// See http://unicode.org/cldr/trac/ticket/2698 and also
		// http://unicode.org/cldr/trac/ticket/1790 for some of the practical
		// implications.
		// TODO: this check could be removed if CLDR adopts this change.
		if c&CLDR == 0 || t.lang != _nb {
			l := normLang(langMacroMap[:], t.lang)
			if l != t.lang {
				changed = true
				t.lang = l
			}
		}
	}
	return t, changed
}

// Canonicalize returns the canonicalized equivalent of the tag.
func (c CanonType) Canonicalize(t Tag) (Tag, error) {
	t, changed := t.canonicalize(c)
	if changed {
		t.remakeString()
	}
	return t, nil
}

// Confidence indicates the level of certainty for a given return value.
// For example, Serbian may be written in Cyrillic or Latin script.
// The confidence level indicates whether a value was explicitly specified,
// whether it is typically the only possible value, or whether there is
// an ambiguity.
type Confidence int

const (
	No    Confidence = iota // full confidence that there was no match
	Low                     // most likely value picked out of a set of alternatives
	High                    // value is generally assumed to be the correct match
	Exact                   // exact match or explicitly specified value
)

var confName = []string{"No", "Low", "High", "Exact"}

func (c Confidence) String() string {
	return confName[c]
}

// remakeString is used to update t.str in case lang, script or region changed.
// It is assumed that pExt and pVariant still point to the start of the
// respective parts.
func (t *Tag) remakeString() {
	if t.str == "" {
		return
	}
	extra := t.str[t.pVariant:]
	if t.pVariant > 0 {
		extra = extra[1:]
	}
	if t.equalTags(und) && strings.HasPrefix(extra, "x-") {
		t.str = extra
		t.pVariant = 0
		t.pExt = 0
		return
	}
	var buf [max99thPercentileSize]byte // avoid extra memory allocation in most cases.
	b := buf[:t.genCoreBytes(buf[:])]
	if extra != "" {
		diff := uint8(len(b)) - t.pVariant
		b = append(b, '-')
		b = append(b, extra...)
		t.pVariant += diff
		t.pExt += uint16(diff)
	} else {
		t.pVariant = uint8(len(b))
		t.pExt = uint16(len(b))
	}
	t.str = string(b)
}

// genCoreBytes writes a string for the base languages, script and region tags
// to the given buffer and returns the number of bytes written. It will never
// write more than maxCoreSize bytes.
func (t *Tag) genCoreBytes(buf []byte) int {
	n := t.lang.stringToBuf(buf[:])
	if t.script != 0 {
		n += copy(buf[n:], "-")
		n += copy(buf[n:], t.script.String())
	}
	if t.region != 0 {
		n += copy(buf[n:], "-")
		n += copy(buf[n:], t.region.String())
	}
	return n
}

// String returns the canonical string representation of the language tag.
func (t Tag) String() string {
	if t.str != "" {
		return t.str
	}
	if t.script == 0 && t.region == 0 {
		return t.lang.String()
	}
	buf := [maxCoreSize]byte{}
	return string(buf[:t.genCoreBytes(buf[:])])
}

// Base returns the base language of the language tag. If the base language is
// unspecified, an attempt will be made to infer it from the context.
// It uses a variant of CLDR's Add Likely Subtags algorithm. This is subject to change.
func (t Tag) Base() (Base, Confidence) {
	if t.lang != 0 {
		return Base{t.lang}, Exact
	}
	c := High
	if t.script == 0 && !(Region{t.region}).IsCountry() {
		c = Low
	}
	if tag, err := addTags(t); err == nil && tag.lang != 0 {
		return Base{tag.lang}, c
	}
	return Base{0}, No
}

// Script infers the script for the language tag. If it was not explicitly given, it will infer
// a most likely candidate.
// If more than one script is commonly used for a language, the most likely one
// is returned with a low confidence indication. For example, it returns (Cyrl, Low)
// for Serbian.
// If a script cannot be inferred (Zzzz, No) is returned. We do not use Zyyy (undetermined)
// as one would suspect from the IANA registry for BCP 47. In a Unicode context Zyyy marks
// common characters (like 1, 2, 3, '.', etc.) and is therefore more like multiple scripts.
// See http://www.unicode.org/reports/tr24/#Values for more details. Zzzz is also used for
// unknown value in CLDR.  (Zzzz, Exact) is returned if Zzzz was explicitly specified.
// Note that an inferred script is never guaranteed to be the correct one. Latin is
// almost exclusively used for Afrikaans, but Arabic has been used for some texts
// in the past.  Also, the script that is commonly used may change over time.
// It uses a variant of CLDR's Add Likely Subtags algorithm. This is subject to change.
func (t Tag) Script() (Script, Confidence) {
	if t.script != 0 {
		return Script{t.script}, Exact
	}
	sc, c := scriptID(_Zzzz), No
	if t.lang < langNoIndexOffset {
		if scr := scriptID(suppressScript[t.lang]); scr != 0 {
			// Note: it is not always the case that a language with a suppress
			// script value is only written in one script (e.g. kk, ms, pa).
			if t.region == 0 {
				return Script{scriptID(scr)}, High
			}
			sc, c = scr, High
		}
	}
	if tag, err := addTags(t); err == nil {
		if tag.script != sc {
			sc, c = tag.script, Low
		}
	} else {
		t, _ = (Deprecated | Macro).Canonicalize(t)
		if tag, err := addTags(t); err == nil && tag.script != sc {
			sc, c = tag.script, Low
		}
	}
	return Script{sc}, c
}

// Region returns the region for the language tag. If it was not explicitly given, it will
// infer a most likely candidate from the context.
// It uses a variant of CLDR's Add Likely Subtags algorithm. This is subject to change.
func (t Tag) Region() (Region, Confidence) {
	if t.region != 0 {
		return Region{t.region}, Exact
	}
	if t, err := addTags(t); err == nil {
		return Region{t.region}, Low // TODO: differentiate between high and low.
	}
	t, _ = (Deprecated | Macro).Canonicalize(t)
	if tag, err := addTags(t); err == nil {
		return Region{tag.region}, Low
	}
	return Region{_ZZ}, No // TODO: return world instead of undetermined?
}

// Variant returns the variants specified explicitly for this language tag.
// or nil if no variant was specified.
func (t Tag) Variants() []Variant {
	v := []Variant{}
	if int(t.pVariant) < int(t.pExt) {
		for x, str := "", t.str[t.pVariant:t.pExt]; str != ""; {
			x, str = nextToken(str)
			v = append(v, Variant{x})
		}
	}
	return v
}

// Parent returns the CLDR parent of t. In CLDR, missing fields in data for a
// specific language are substituted with fields from the parent language.
// The parent for a language may change for newer versions of CLDR.
func (t Tag) Parent() Tag {
	if t.str != "" {
		// Strip the variants and extensions.
		t, _ = Raw.Compose(t.Raw())
		if t.region == 0 && t.script != 0 && t.lang != 0 {
			base, _ := addTags(Tag{lang: t.lang})
			if base.script == t.script {
				return Tag{lang: t.lang}
			}
		}
		return t
	}
	if t.lang != 0 {
		if t.region != 0 {
			maxScript := t.script
			if maxScript == 0 {
				max, _ := addTags(t)
				maxScript = max.script
			}

			for i := range parents {
				if langID(parents[i].lang) == t.lang && scriptID(parents[i].maxScript) == maxScript {
					for _, r := range parents[i].fromRegion {
						if regionID(r) == t.region {
							return Tag{
								lang:   t.lang,
								script: scriptID(parents[i].script),
								region: regionID(parents[i].toRegion),
							}
						}
					}
				}
			}

			// Strip the script if it is the default one.
			base, _ := addTags(Tag{lang: t.lang})
			if base.script != maxScript {
				return Tag{lang: t.lang, script: maxScript}
			}
			return Tag{lang: t.lang}
		} else if t.script != 0 {
			// The parent for an base-script pair with a non-default script is
			// "und" instead of the base language.
			base, _ := addTags(Tag{lang: t.lang})
			if base.script != t.script {
				return und
			}
			return Tag{lang: t.lang}
		}
	}
	return und
}

// returns token t and the rest of the string.
func nextToken(s string) (t, tail string) {
	p := strings.Index(s[1:], "-")
	if p == -1 {
		return s[1:], ""
	}
	p++
	return s[1:p], s[p:]
}

// Extension is a single BCP 47 extension.
type Extension struct {
	s string
}

// String returns the string representation of the extension, including the
// type tag.
func (e Extension) String() string {
	return e.s
}

// ParseExtension parses s as an extension and returns it on success.
func ParseExtension(s string) (e Extension, err error) {
	scan := makeScannerString(s)
	var end int
	if n := len(scan.token); n != 1 {
		return Extension{}, errSyntax
	}
	scan.toLower(0, len(scan.b))
	end = parseExtension(&scan)
	if end != len(s) {
		return Extension{}, errSyntax
	}
	return Extension{string(scan.b)}, nil
}

// Type returns the one-byte extension type of e. It returns 0 for the zero
// exception.
func (e Extension) Type() byte {
	if e.s == "" {
		return 0
	}
	return e.s[0]
}

// Tokens returns the list of tokens of e.
func (e Extension) Tokens() []string {
	return strings.Split(e.s, "-")
}

// Extension returns the extension of type x for tag t. It will return
// false for ok if t does not have the requested extension. The returned
// extension will be invalid in this case.
func (t Tag) Extension(x byte) (ext Extension, ok bool) {
	for i := int(t.pExt); i < len(t.str)-1; {
		var ext string
		i, ext = getExtension(t.str, i)
		if ext[0] == x {
			return Extension{ext}, true
		}
	}
	return Extension{string(x)}, false
}

// Extensions returns all extensions of t.
func (t Tag) Extensions() []Extension {
	e := []Extension{}
	for i := int(t.pExt); i < len(t.str)-1; {
		var ext string
		i, ext = getExtension(t.str, i)
		e = append(e, Extension{ext})
	}
	return e
}

// TypeForKey returns the type associated with the given key, where key and type
// are of the allowed values defined for the Unicode locale extension ('u') in
// http://www.unicode.org/reports/tr35/#Unicode_Language_and_Locale_Identifiers.
// TypeForKey will traverse the inheritance chain to get the correct value.
func (t Tag) TypeForKey(key string) string {
	if start, end, _ := t.findTypeForKey(key); end != start {
		return t.str[start:end]
	}
	return ""
}

var (
	errPrivateUse       = errors.New("cannot set a key on a private use tag")
	errInvalidArguments = errors.New("invalid key or type")
)

// SetTypeForKey returns a new Tag with the key set to type, where key and type
// are of the allowed values defined for the Unicode locale extension ('u') in
// http://www.unicode.org/reports/tr35/#Unicode_Language_and_Locale_Identifiers.
// An empty value removes an existing pair with the same key.
func (t Tag) SetTypeForKey(key, value string) (Tag, error) {
	if t.private() {
		return t, errPrivateUse
	}
	if len(key) != 2 {
		return t, errInvalidArguments
	}

	// Remove the setting if value is "".
	if value == "" {
		start, end, _ := t.findTypeForKey(key)
		if start != end {
			// Remove key tag and leading '-'.
			start -= 4

			// Remove a possible empty extension.
			if (end == len(t.str) || t.str[end+2] == '-') && t.str[start-2] == '-' {
				start -= 2
			}
			if start == int(t.pVariant) && end == len(t.str) {
				t.str = ""
				t.pVariant, t.pExt = 0, 0
			} else {
				t.str = fmt.Sprintf("%s%s", t.str[:start], t.str[end:])
			}
		}
		return t, nil
	}

	if len(value) < 3 || len(value) > 8 {
		return t, errInvalidArguments
	}

	var (
		buf    [maxCoreSize + maxSimpleUExtensionSize]byte
		uStart int // start of the -u extension.
	)

	// Generate the tag string if needed.
	if t.str == "" {
		uStart = t.genCoreBytes(buf[:])
		buf[uStart] = '-'
		uStart++
	}

	// Create new key-type pair and parse it to verify.
	b := buf[uStart:]
	copy(b, "u-")
	copy(b[2:], key)
	b[4] = '-'
	b = b[:5+copy(b[5:], value)]
	scan := makeScanner(b)
	if parseExtensions(&scan); scan.err != nil {
		return t, scan.err
	}

	// Assemble the replacement string.
	if t.str == "" {
		t.pVariant, t.pExt = byte(uStart-1), uint16(uStart-1)
		t.str = string(buf[:uStart+len(b)])
	} else {
		s := t.str
		start, end, hasExt := t.findTypeForKey(key)
		if start == end {
			if hasExt {
				b = b[2:]
			}
			t.str = fmt.Sprintf("%s-%s%s", s[:start], b, s[end:])
		} else {
			t.str = fmt.Sprintf("%s%s%s", s[:start], value, s[end:])
		}
	}
	return t, nil
}

// findKeyAndType returns the start and end position for the type corresponding
// to key or the point at which to insert the key-value pair if the type
// wasn't found. The hasExt return value reports whether an -u extension was present.
// Note: the extensions are typically very small and are likely to contain
// only one key-type pair.
func (t Tag) findTypeForKey(key string) (start, end int, hasExt bool) {
	p := int(t.pExt)
	if len(key) != 2 || p == len(t.str) || p == 0 {
		return p, p, false
	}
	s := t.str

	// Find the correct extension.
	for p++; s[p] != 'u'; p++ {
		if s[p] > 'u' {
			p--
			return p, p, false
		}
		if p = nextExtension(s, p); p == len(s) {
			return len(s), len(s), false
		}
	}
	// Proceed to the hyphen following the extension name.
	p++

	// curKey is the key currently being processed.
	curKey := ""

	// Iterate over keys until we get the end of a section.
	for {
		// p points to the hyphen preceding the current token.
		if p3 := p + 3; s[p3] == '-' {
			// Found a key.
			// Check whether we just processed the key that was requested.
			if curKey == key {
				return start, p, true
			}
			// Set to the next key and continue scanning type tokens.
			curKey = s[p+1 : p3]
			if curKey > key {
				return p, p, true
			}
			// Start of the type token sequence.
			start = p + 4
			// A type is at least 3 characters long.
			p += 7 // 4 + 3
		} else {
			// Attribute or type, which is at least 3 characters long.
			p += 4
		}
		// p points past the third character of a type or attribute.
		max := p + 5 // maximum length of token plus hyphen.
		if len(s) < max {
			max = len(s)
		}
		for ; p < max && s[p] != '-'; p++ {
		}
		// Bail if we have exhausted all tokens or if the next token starts
		// a new extension.
		if p == len(s) || s[p+2] == '-' {
			if curKey == key {
				return start, p, true
			}
			return p, p, true
		}
	}
}

// Base is an ISO 639 language code, used for encoding the base language
// of a language tag.
type Base struct {
	langID
}

// ParseBase parses a 2- or 3-letter ISO 639 code.
// It returns a ValueError if s is a well-formed but unknown language identifier
// or another error if another error occurred.
func ParseBase(s string) (Base, error) {
	if n := len(s); n < 2 || 3 < n {
		return Base{}, errSyntax
	}
	var buf [3]byte
	l, err := getLangID(buf[:copy(buf[:], s)])
	return Base{l}, err
}

// Script is a 4-letter ISO 15924 code for representing scripts.
// It is idiomatically represented in title case.
type Script struct {
	scriptID
}

// ParseScript parses a 4-letter ISO 15924 code.
// It returns a ValueError if s is a well-formed but unknown script identifier
// or another error if another error occurred.
func ParseScript(s string) (Script, error) {
	if len(s) != 4 {
		return Script{}, errSyntax
	}
	var buf [4]byte
	sc, err := getScriptID(script, buf[:copy(buf[:], s)])
	return Script{sc}, err
}

// Region is an ISO 3166-1 or UN M.49 code for representing countries and regions.
type Region struct {
	regionID
}

// EncodeM49 returns the Region for the given UN M.49 code.
// It returns an error if r is not a valid code.
func EncodeM49(r int) (Region, error) {
	rid, err := getRegionM49(r)
	return Region{rid}, err
}

// ParseRegion parses a 2- or 3-letter ISO 3166-1 or a UN M.49 code.
// It returns a ValueError if s is a well-formed but unknown region identifier
// or another error if another error occurred.
func ParseRegion(s string) (Region, error) {
	if n := len(s); n < 2 || 3 < n {
		return Region{}, errSyntax
	}
	var buf [3]byte
	r, err := getRegionID(buf[:copy(buf[:], s)])
	return Region{r}, err
}

// IsCountry returns whether this region is a country or autonomous area.
func (r Region) IsCountry() bool {
	if r.regionID < isoRegionOffset || r.IsPrivateUse() {
		return false
	}
	return true
}

// Contains returns whether Region c is contained by Region r. It returns true
// if c == r.
func (r Region) Contains(c Region) bool {
	return r.regionID.contains(c.regionID)
}

func (r regionID) contains(c regionID) bool {
	if r == c {
		return true
	}
	g := regionInclusion[r]
	if g >= nRegionGroups {
		return false
	}
	m := regionContainment[g]

	d := regionInclusion[c]
	b := regionInclusionBits[d]

	// A contained country may belong to multiple disjoint groups. Matching any
	// of these indicates containment. If the contained region is a group, it
	// must strictly be a subset.
	if d >= nRegionGroups {
		return b&m != 0
	}
	return b&^m == 0
}

// Variant represents a registered variant of a language as defined by BCP 47.
type Variant struct {
	variant string
}

// ParseVariant parses and returns a Variant. An error is returned if s is not
// a valid variant.
func ParseVariant(s string) (Variant, error) {
	s = strings.ToLower(s)
	if _, ok := variantIndex[s]; ok {
		return Variant{s}, nil
	}
	return Variant{}, mkErrInvalid([]byte(s))
}

// String returns the string representation of the variant.
func (v Variant) String() string {
	return v.variant
}

// Currency is an ISO 4217 currency designator.
type Currency struct {
	currencyID
}

// ParseCurrency parses a 3-letter ISO 4217 code.
// It returns a ValueError if s is a well-formed but unknown currency identifier
// or another error if another error occurred.
func ParseCurrency(s string) (Currency, error) {
	if len(s) != 3 {
		return Currency{}, errSyntax
	}
	var buf [3]byte
	c, err := getCurrencyID(currency, buf[:copy(buf[:], s)])
	return Currency{c}, err
}
