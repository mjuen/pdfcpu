/*
Copyright 2018 The pdfcpu Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package model

import (
	"encoding/hex"
	"strconv"
	"strings"
	"unicode"

	"github.com/mjuen/pdfcpu/pkg/log"
	"github.com/mjuen/pdfcpu/pkg/pdfcpu/types"
	"github.com/pkg/errors"
)

var (
	errArrayCorrupt            = errors.New("pdfcpu: parse: corrupt array")
	errArrayNotTerminated      = errors.New("pdfcpu: parse: unterminated array")
	errDictionaryCorrupt       = errors.New("pdfcpu: parse: corrupt dictionary")
	errDictionaryDuplicateKey  = errors.New("pdfcpu: parse: duplicate key")
	errDictionaryNotTerminated = errors.New("pdfcpu: parse: unterminated dictionary")
	errHexLiteralCorrupt       = errors.New("pdfcpu: parse: corrupt hex literal")
	errHexLiteralNotTerminated = errors.New("pdfcpu: parse: hex literal not terminated")
	errNameObjectCorrupt       = errors.New("pdfcpu: parse: corrupt name object")
	errNoArray                 = errors.New("pdfcpu: parse: no array")
	errNoDictionary            = errors.New("pdfcpu: parse: no dictionary")
	errStringLiteralCorrupt    = errors.New("pdfcpu: parse: corrupt string literal, possibly unbalanced parenthesis")
	errBufNotAvailable         = errors.New("pdfcpu: parse: no buffer available")
	errXrefStreamMissingW      = errors.New("pdfcpu: parse: xref stream dict missing entry W")
	errXrefStreamCorruptW      = errors.New("pdfcpu: parse: xref stream dict corrupt entry W: expecting array of 3 int")
	errXrefStreamCorruptIndex  = errors.New("pdfcpu: parse: xref stream dict corrupt entry Index")
	errObjStreamMissingN       = errors.New("pdfcpu: parse: obj stream dict missing entry W")
	errObjStreamMissingFirst   = errors.New("pdfcpu: parse: obj stream dict missing entry First")
)

func positionToNextWhitespace(s string) (int, string) {
	for i, c := range s {
		if unicode.IsSpace(c) || c == 0x00 {
			return i, s[i:]
		}
	}
	return 0, s
}

// PositionToNextWhitespaceOrChar trims a string to next whitespace or one of given chars.
// Returns the index of the position or -1 if no match.
func positionToNextWhitespaceOrChar(s, chars string) (int, string) {
	if len(chars) == 0 {
		return positionToNextWhitespace(s)
	}

	for i, c := range s {
		for _, m := range chars {
			if c == m || unicode.IsSpace(c) || c == 0x00 {
				return i, s[i:]
			}
		}
	}

	return -1, s
}

func positionToNextEOL(s string) string {
	for i, c := range s {
		for _, m := range "\x0A\x0D" {
			if c == m {
				return s[i:]
			}
		}
	}
	return ""
}

// trimLeftSpace trims leading whitespace and trailing comment.
func trimLeftSpace(s string, relaxed bool) (string, bool) {
	if log.ParseEnabled() {
		log.Parse.Printf("TrimLeftSpace: begin %s\n", s)
	}

	whitespace := func(c rune) bool { return unicode.IsSpace(c) || c == 0x00 }

	whitespaceNoEol := func(r rune) bool {
		switch r {
		case '\t', '\v', '\f', ' ', 0x85, 0xA0, 0x00:
			return true
		}
		return false
	}

	var eol bool

	for {
		if relaxed {
			s = strings.TrimLeftFunc(s, whitespaceNoEol)
			if len(s) >= 1 && (s[0] == '\n' || s[0] == '\r') {
				eol = true
			}
		}
		s = strings.TrimLeftFunc(s, whitespace)
		if log.ParseEnabled() {
			log.Parse.Printf("1 outstr: <%s>\n", s)
		}
		if len(s) <= 1 || s[0] != '%' {
			break
		}
		// trim PDF comment (= '%' up to eol)
		s = positionToNextEOL(s)
		if log.ParseEnabled() {
			log.Parse.Printf("2 outstr: <%s>\n", s)
		}
	}

	if log.ParseEnabled() {
		log.Parse.Printf("TrimLeftSpace: end %s\n", s)
	}

	return s, eol
}

// HexString validates and formats a hex string to be of even length.
func hexString(s string) (*string, bool) {
	if len(s) == 0 {
		s1 := ""
		return &s1, true
	}

	var sb strings.Builder
	i := 0

	for _, c := range strings.ToUpper(s) {
		if strings.ContainsRune(" \x09\x0A\x0C\x0D", c) {
			if i%2 > 0 {
				sb.WriteString("0")
				i = 0
			}
			continue
		}
		isHexChar := false
		for _, hexch := range "ABCDEF1234567890" {
			if c == hexch {
				isHexChar = true
				sb.WriteRune(c)
				i++
				break
			}
		}
		if !isHexChar {
			return nil, false
		}
	}

	// If the final digit of a hexadecimal string is missing -
	// that is, if there is an odd number of digits - the final digit shall be assumed to be 0.
	if i%2 > 0 {
		sb.WriteString("0")
	}

	ss := sb.String()
	return &ss, true
}

// balancedParenthesesPrefix returns the index of the end position of the balanced parentheses prefix of s
// or -1 if unbalanced. s has to start with '('
func balancedParenthesesPrefix(s string) int {
	var j int
	escaped := false

	for i := 0; i < len(s); i++ {

		c := s[i]

		if !escaped && c == '\\' {
			escaped = true
			continue
		}

		if escaped {
			escaped = false
			continue
		}

		if c == '(' {
			j++
		}

		if c == ')' {
			j--
		}

		if j == 0 {
			return i
		}

	}

	return -1
}

func forwardParseBuf(buf string, pos int) string {
	if pos < len(buf) {
		return buf[pos:]
	}
	return ""
}

func delimiter(b byte) bool {
	s := "<>[]()/"
	for i := 0; i < len(s); i++ {
		if b == s[i] {
			return true
		}
	}
	return false
}

// ParseObjectAttributes parses object number and generation of the next object for given string buffer.
func ParseObjectAttributes(line *string) (objectNumber *int, generationNumber *int, err error) {

	if line == nil || len(*line) == 0 {
		return nil, nil, errors.New("pdfcpu: ParseObjectAttributes: buf not available")
	}

	if log.ParseEnabled() {
		log.Parse.Printf("ParseObjectAttributes: buf=<%s>\n", *line)
	}

	l := *line
	var remainder string

	i := strings.Index(l, "obj")
	if i < 0 {
		return nil, nil, errors.New("pdfcpu: ParseObjectAttributes: can't find \"obj\"")
	}

	remainder = l[i+len("obj"):]
	l = l[:i]

	// object number

	l, _ = trimLeftSpace(l, false)
	if len(l) == 0 {
		return nil, nil, errors.New("pdfcpu: ParseObjectAttributes: can't find object number")
	}

	i, _ = positionToNextWhitespaceOrChar(l, "%")
	if i <= 0 {
		return nil, nil, errors.New("pdfcpu: ParseObjectAttributes: can't find end of object number")
	}

	objNr, err := strconv.Atoi(l[:i])
	if err != nil {
		return nil, nil, err
	}

	// generation number

	l = l[i:]
	l, _ = trimLeftSpace(l, false)
	if len(l) == 0 {
		return nil, nil, errors.New("pdfcpu: ParseObjectAttributes: can't find generation number")
	}

	i, _ = positionToNextWhitespaceOrChar(l, "%")
	if i <= 0 {
		return nil, nil, errors.New("pdfcpu: ParseObjectAttributes: can't find end of generation number")
	}

	genNr, err := strconv.Atoi(l[:i])
	if err != nil {
		return nil, nil, err
	}

	objectNumber = &objNr
	generationNumber = &genNr

	*line = remainder

	return objectNumber, generationNumber, nil
}

func parseArray(line *string) (*types.Array, error) {
	if log.ParseEnabled() {
		log.Parse.Println("ParseObject: value = Array")
	}
	if line == nil || len(*line) == 0 {
		return nil, errNoArray
	}

	l := *line

	if log.ParseEnabled() {
		log.Parse.Printf("ParseArray: %s\n", l)
	}

	if !strings.HasPrefix(l, "[") {
		return nil, errArrayCorrupt
	}

	if len(l) == 1 {
		return nil, errArrayNotTerminated
	}

	// position behind '['
	l = forwardParseBuf(l, 1)

	// position to first non whitespace char after '['
	l, _ = trimLeftSpace(l, false)

	if len(l) == 0 {
		// only whitespace after '['
		return nil, errArrayNotTerminated
	}

	a := types.Array{}

	for !strings.HasPrefix(l, "]") {

		obj, err := ParseObject(&l)
		if err != nil {
			return nil, err
		}
		if log.ParseEnabled() {
			log.Parse.Printf("ParseArray: new array obj=%v\n", obj)
		}
		a = append(a, obj)

		// we are positioned on the char behind the last parsed array entry.
		if len(l) == 0 {
			return nil, errArrayNotTerminated
		}

		// position to next non whitespace char.
		l, _ = trimLeftSpace(l, false)
		if len(l) == 0 {
			return nil, errArrayNotTerminated
		}
	}

	// position behind ']'
	l = forwardParseBuf(l, 1)

	*line = l

	if log.ParseEnabled() {
		log.Parse.Printf("ParseArray: returning array (len=%d): %v\n", len(a), a)
	}

	return &a, nil
}

func parseStringLiteral(line *string) (types.Object, error) {
	// Balanced pairs of parenthesis are allowed.
	// Empty literals are allowed.
	// \ needs special treatment.
	// Allowed escape sequences:
	// \n	x0A
	// \r	x0D
	// \t	x09
	// \b	x08
	// \f	xFF
	// \(	x28
	// \)	x29
	// \\	x5C
	// \ddd octal code sequence, d=0..7

	// Ignore '\' for undefined escape sequences.

	// Unescaped 0x0A,0x0D or combination gets parsed as 0x0A.

	// Join split lines by '\' eol.

	if line == nil || len(*line) == 0 {
		return nil, errBufNotAvailable
	}

	if log.ParseEnabled() {
		log.Parse.Printf("ParseObject: value = String Literal: <%s>\n", *line)
	}

	l := *line

	if log.ParseEnabled() {
		log.Parse.Printf("parseStringLiteral: begin <%s>\n", l)
	}

	if len(l) < 2 || !strings.HasPrefix(l, "(") {
		return nil, errStringLiteralCorrupt
	}

	// Calculate prefix with balanced parentheses,
	// return index of enclosing ')'.
	i := balancedParenthesesPrefix(l)
	if i < 0 {
		// No balanced parentheses.
		return nil, errStringLiteralCorrupt
	}

	// remove enclosing '(', ')'
	balParStr := l[1:i]

	// Parse string literal, see 7.3.4.2
	//str := stringLiteral(balParStr)

	// position behind ')'
	*line = forwardParseBuf(l[i:], 1)

	stringLiteral := types.StringLiteral(balParStr)
	if log.ParseEnabled() {
		log.Parse.Printf("parseStringLiteral: end <%s>\n", stringLiteral)
	}

	return stringLiteral, nil
}

func parseHexLiteral(line *string) (types.Object, error) {
	if line == nil || len(*line) == 0 {
		return nil, errBufNotAvailable
	}

	l := *line

	if log.ParseEnabled() {
		log.Parse.Printf("parseHexLiteral: %s\n", l)
	}

	if len(l) < 2 || !strings.HasPrefix(l, "<") {
		return nil, errHexLiteralCorrupt
	}

	// position behind '<'
	l = forwardParseBuf(l, 1)

	eov := strings.Index(l, ">") // end of hex literal.
	if eov < 0 {
		return nil, errHexLiteralNotTerminated
	}

	hexStr, ok := hexString(strings.TrimSpace(l[:eov]))
	if !ok {
		return nil, errHexLiteralCorrupt
	}

	// position behind '>'
	*line = forwardParseBuf(l[eov:], 1)

	return types.HexLiteral(*hexStr), nil
}

func validateNameHexSequence(s string) error {
	for i := 0; i < len(s); {
		c := s[i]
		if c != '#' {
			i++
			continue
		}

		// # detected, next 2 chars have to exist.
		if len(s) < i+3 {
			return errNameObjectCorrupt
		}

		s1 := s[i+1 : i+3]

		// And they have to be hex characters.
		_, err := hex.DecodeString(s1)
		if err != nil {
			return errNameObjectCorrupt
		}

		i += 3
	}
	return nil
}

func parseName(line *string) (*types.Name, error) {
	// see 7.3.5
	if log.ParseEnabled() {
		log.Parse.Println("ParseObject: value = Name Object")
	}
	if line == nil || len(*line) == 0 {
		return nil, errBufNotAvailable
	}

	l := *line

	if log.ParseEnabled() {
		log.Parse.Printf("parseNameObject: %s\n", l)
	}
	if len(l) < 2 || !strings.HasPrefix(l, "/") {
		return nil, errNameObjectCorrupt
	}

	// position behind '/'
	l = forwardParseBuf(l, 1)

	// cut off on whitespace or delimiter
	eok, _ := positionToNextWhitespaceOrChar(l, "/<>()[]%")
	if eok < 0 {
		// Name terminated by eol.
		*line = ""
	} else {
		*line = l[eok:]
		l = l[:eok]
	}

	// Validate optional #xx sequences
	err := validateNameHexSequence(l)
	if err != nil {
		return nil, err
	}

	nameObj := types.Name(l)
	return &nameObj, nil
}

func processDictKeys(line *string, relaxed bool) (types.Dict, error) {
	l := *line
	var eol bool
	d := types.NewDict()
	for !strings.HasPrefix(l, ">>") {
		key, err := parseName(&l)
		if err != nil {
			return nil, err
		}
		if log.ParseEnabled() {
			log.Parse.Printf("ParseDict: key = %s\n", key)
		}

		// position to first non whitespace after key
		l, eol = trimLeftSpace(l, relaxed)

		if len(l) == 0 {
			if log.ParseEnabled() {
				log.Parse.Println("ParseDict: only whitespace after key")
			}
			// only whitespace after key
			return nil, errDictionaryNotTerminated
		}

		// Fix for #252:
		// For dicts with kv pairs terminated by eol we accept a missing value as an empty string.
		if eol {
			obj := types.StringLiteral("")
			if log.ParseEnabled() {
				log.Parse.Printf("ParseDict: dict[%s]=%v\n", key, obj)
			}
			if ok := d.Insert(string(*key), obj); !ok {
				return nil, errDictionaryDuplicateKey
			}
			continue
		}

		obj, err := ParseObject(&l)
		if err != nil {
			return nil, err
		}

		// Specifying the null object as the value of a dictionary entry (7.3.7, "Dictionary Objects")
		// hall be equivalent to omitting the entry entirely.
		if obj != nil {
			d.Insert(string(*key), obj)
			if log.ParseEnabled() {
				log.Parse.Printf("ParseDict: dict[%s]=%v\n", key, obj)
			}
			// if ok := d.Insert(string(*key), obj); !ok {
			// 	return nil, errDictionaryDuplicateKey
			// }
		}

		// We are positioned on the char behind the last parsed dict value.
		if len(l) == 0 {
			return nil, errDictionaryNotTerminated
		}

		// Position to next non whitespace char.
		l, _ = trimLeftSpace(l, false)
		if len(l) == 0 {
			return nil, errDictionaryNotTerminated
		}

	}
	*line = l
	return d, nil
}

func parseDict(line *string, relaxed bool) (types.Dict, error) {
	if line == nil || len(*line) == 0 {
		return nil, errNoDictionary
	}

	l := *line

	if log.ParseEnabled() {
		log.Parse.Printf("ParseDict: %s\n", l)
	}

	if len(l) < 4 || !strings.HasPrefix(l, "<<") {
		return nil, errDictionaryCorrupt
	}

	// position behind '<<'
	l = forwardParseBuf(l, 2)

	// position to first non whitespace char after '<<'
	l, _ = trimLeftSpace(l, false)

	if len(l) == 0 {
		// only whitespace after '['
		return nil, errDictionaryNotTerminated
	}

	d, err := processDictKeys(&l, relaxed)
	if err != nil {
		return nil, err
	}

	// position behind '>>'
	l = forwardParseBuf(l, 2)

	*line = l

	if log.ParseEnabled() {
		log.Parse.Printf("ParseDict: returning dict at: %v\n", d)
	}

	return d, nil
}

func noBuf(l *string) bool {
	return l == nil || len(*l) == 0
}

func startParseNumericOrIndRef(l string) (string, string, int) {
	i1, _ := positionToNextWhitespaceOrChar(l, "/<([]>%")
	var l1 string
	if i1 > 0 {
		l1 = l[i1:]
	} else {
		l1 = l[len(l):]
	}

	str := l
	if i1 > 0 {
		str = l[:i1]
	}

	/*
		Integers are sometimes prefixed with any form of 0.
		Following is a list of valid prefixes that can be safely ignored:
			0
			0.000000000
	*/
	if len(str) > 1 && str[0] == '0' {
		if str[1] == '+' || str[1] == '-' {
			str = str[1:]
		} else if str[1] == '.' {
			var i int
			for i = 2; len(str) > i && str[i] == '0'; i++ {
			}
			if len(str) > i && (str[i] == '+' || str[i] == '-') {
				str = str[i:]
			}
		}
	}
	return str, l1, i1
}

func isRangeError(err error) bool {
	if err, ok := err.(*strconv.NumError); ok {
		if err.Err == strconv.ErrRange {
			return true
		}
	}
	return false
}

func parseIndRef(s, l, l1 string, line *string, i, i2 int, rangeErr bool) (types.Object, error) {

	g, err := strconv.Atoi(s)
	if err != nil {
		// 2nd int(generation number) not available.
		// Can't be an indirect reference.
		if log.ParseEnabled() {
			log.Parse.Printf("parseIndRef: 3 objects, 2nd no int, value is no indirect ref but numeric int: %d\n", i)
		}
		*line = l1
		return types.Integer(i), nil
	}

	l = l[i2:]
	l, _ = trimLeftSpace(l, false)

	if len(l) == 0 {
		if rangeErr {
			return nil, err
		}
		// only whitespace
		*line = l1
		return types.Integer(i), nil
	}

	if l[0] == 'R' {
		*line = forwardParseBuf(l, 1)
		if rangeErr {
			return nil, nil
		}
		// We have all 3 components to create an indirect reference.
		return *types.NewIndirectRef(i, g), nil
	}

	if rangeErr {
		return nil, err
	}

	// 'R' not available.
	// Can't be an indirect reference.
	if log.ParseEnabled() {
		log.Parse.Printf("parseNumericOrIndRef: value is no indirect ref(no 'R') but numeric int: %d\n", i)
	}
	*line = l1

	return types.Integer(i), nil
}

func parseFloat(s string) (types.Object, error) {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil, err
	}

	if log.ParseEnabled() {
		log.Parse.Printf("parseFloat: value is: %f\n", f)
	}

	return types.Float(f), nil
}

func parseNumericOrIndRef(line *string) (types.Object, error) {
	if noBuf(line) {
		return nil, errBufNotAvailable
	}

	l := *line

	// if this object is an integer we need to check for an indirect reference eg. 1 0 R
	// otherwise it has to be a float
	// we have to check first for integer
	s, l1, i1 := startParseNumericOrIndRef(l)

	// Try int
	var rangeErr bool
	i, err := strconv.Atoi(s)
	if err != nil {
		rangeErr = isRangeError(err)
		if !rangeErr {
			// Try float
			*line = l1
			return parseFloat(s)
		}

		// #407
		i = 0
	}

	// We have an Int!

	// if not followed by whitespace return sole integer value.
	if i1 <= 0 || delimiter(l[i1]) {

		if rangeErr {
			return nil, err
		}

		if log.ParseEnabled() {
			log.Parse.Printf("parseNumericOrIndRef: value is numeric int: %d\n", i)
		}
		*line = l1
		return types.Integer(i), nil
	}

	// Must be indirect reference. (123 0 R)
	// Missing is the 2nd int and "R".

	l = l[i1:]
	l, _ = trimLeftSpace(l, false)
	if len(l) == 0 {
		// only whitespace
		if rangeErr {
			return nil, err
		}
		*line = l1
		return types.Integer(i), nil
	}

	i2, _ := positionToNextWhitespaceOrChar(l, "/<([]>")

	// if only 2 token, can't be indirect reference.
	// if not followed by whitespace return sole integer value.
	if i2 <= 0 || delimiter(l[i2]) {
		if rangeErr {
			return nil, err
		}
		if log.ParseEnabled() {
			log.Parse.Printf("parseNumericOrIndRef: 2 objects => value is numeric int: %d\n", i)
		}
		*line = l1
		return types.Integer(i), nil
	}

	s = l
	if i2 > 0 {
		s = l[:i2]
	}

	return parseIndRef(s, l, l1, line, i, i2, rangeErr)
}

func parseHexLiteralOrDict(l *string) (val types.Object, err error) {
	if len(*l) < 2 {
		return nil, errBufNotAvailable
	}

	// if next char = '<' parseDict.
	if (*l)[1] == '<' {
		if log.ParseEnabled() {
			log.Parse.Println("parseHexLiteralOrDict: value = Dictionary")
		}
		var (
			d   types.Dict
			err error
		)
		if d, err = parseDict(l, false); err != nil {
			if d, err = parseDict(l, true); err != nil {
				return nil, err
			}
		}
		val = d
	} else {
		// hex literals
		if log.ParseEnabled() {
			log.Parse.Println("parseHexLiteralOrDict: value = Hex Literal")
		}
		if val, err = parseHexLiteral(l); err != nil {
			return nil, err
		}
	}

	return val, nil
}

func parseBooleanOrNull(l string) (val types.Object, s string, ok bool) {
	// null, absent object
	if strings.HasPrefix(l, "null") {
		if log.ParseEnabled() {
			log.Parse.Println("parseBoolean: value = null")
		}
		return nil, "null", true
	}

	// boolean true
	if strings.HasPrefix(l, "true") {
		if log.ParseEnabled() {
			log.Parse.Println("parseBoolean: value = true")
		}
		return types.Boolean(true), "true", true
	}

	// boolean false
	if strings.HasPrefix(l, "false") {
		if log.ParseEnabled() {
			log.Parse.Println("parseBoolean: value = false")
		}
		return types.Boolean(false), "false", true
	}

	return nil, "", false
}

// ParseObject parses next Object from string buffer and returns the updated (left clipped) buffer.
func ParseObject(line *string) (types.Object, error) {
	if noBuf(line) {
		return nil, errBufNotAvailable
	}

	l := *line

	if log.ParseEnabled() {
		log.Parse.Printf("ParseObject: buf= <%s>\n", l)
	}

	// position to first non whitespace char
	l, _ = trimLeftSpace(l, false)
	if len(l) == 0 {
		// only whitespace
		return nil, errBufNotAvailable
	}

	var value types.Object
	var err error

	switch l[0] {

	case '[': // array
		a, err := parseArray(&l)
		if err != nil {
			return nil, err
		}
		value = *a

	case '/': // name
		nameObj, err := parseName(&l)
		if err != nil {
			return nil, err
		}
		value = *nameObj

	case '<': // hex literal or dict
		value, err = parseHexLiteralOrDict(&l)
		if err != nil {
			return nil, err
		}

	case '(': // string literal
		if value, err = parseStringLiteral(&l); err != nil {
			return nil, err
		}

	default:
		var valStr string
		var ok bool
		value, valStr, ok = parseBooleanOrNull(l)
		if ok {
			l = forwardParseBuf(l, len(valStr))
			break
		}
		// Must be numeric or indirect reference:
		// int 0 r
		// int
		// float
		if value, err = parseNumericOrIndRef(&l); err != nil {
			return nil, err
		}

	}

	if log.ParseEnabled() {
		log.Parse.Printf("ParseObject returning %v\n", value)
	}

	*line = l

	return value, nil
}

func createXRefStreamDict(sd *types.StreamDict, objs []int) (*types.XRefStreamDict, error) {
	// Read parameter W in order to decode the xref table.
	// array of integers representing the size of the fields in a single cross-reference entry.

	var wIntArr [3]int

	a := sd.W()
	if a == nil {
		return nil, errXrefStreamMissingW
	}

	// validate array with 3 positive integers
	if len(a) != 3 {
		return nil, errXrefStreamCorruptW
	}

	f := func(ok bool, i int) bool {
		return !ok || i < 0
	}

	i1, ok := a[0].(types.Integer)
	if f(ok, i1.Value()) {
		return nil, errXrefStreamCorruptW
	}
	wIntArr[0] = int(i1)

	i2, ok := a[1].(types.Integer)
	if f(ok, i2.Value()) {
		return nil, errXrefStreamCorruptW
	}
	wIntArr[1] = int(i2)

	i3, ok := a[2].(types.Integer)
	if f(ok, i3.Value()) {
		return nil, errXrefStreamCorruptW
	}
	wIntArr[2] = int(i3)

	return &types.XRefStreamDict{
		StreamDict:     *sd,
		Size:           *sd.Size(),
		Objects:        objs,
		W:              wIntArr,
		PreviousOffset: sd.Prev(),
	}, nil
}

// ParseXRefStreamDict creates a XRefStreamDict out of a StreamDict.
func ParseXRefStreamDict(sd *types.StreamDict) (*types.XRefStreamDict, error) {
	if log.ParseEnabled() {
		log.Parse.Println("ParseXRefStreamDict: begin")
	}
	if sd.Size() == nil {
		return nil, errors.New("pdfcpu: ParseXRefStreamDict: \"Size\" not available")
	}

	objs := []int{}

	//	Read optional parameter Index
	indArr := sd.Index()
	if indArr != nil {
		if log.ParseEnabled() {
			log.Parse.Println("ParseXRefStreamDict: using index dict")
		}

		if len(indArr)%2 > 1 {
			return nil, errXrefStreamCorruptIndex
		}

		for i := 0; i < len(indArr)/2; i++ {

			startObj, ok := indArr[i*2].(types.Integer)
			if !ok {
				return nil, errXrefStreamCorruptIndex
			}

			count, ok := indArr[i*2+1].(types.Integer)
			if !ok {
				return nil, errXrefStreamCorruptIndex
			}

			for j := 0; j < count.Value(); j++ {
				objs = append(objs, startObj.Value()+j)
			}
		}

	} else {
		if log.ParseEnabled() {
			log.Parse.Println("ParseXRefStreamDict: no index dict")
		}
		for i := 0; i < *sd.Size(); i++ {
			objs = append(objs, i)

		}
	}

	xsd, err := createXRefStreamDict(sd, objs)
	if err != nil {
		return nil, err
	}

	if log.ParseEnabled() {
		log.Parse.Println("ParseXRefStreamDict: end")
	}

	return xsd, nil
}

// ObjectStreamDict creates a ObjectStreamDict out of a StreamDict.
func ObjectStreamDict(sd *types.StreamDict) (*types.ObjectStreamDict, error) {
	if sd.First() == nil {
		return nil, errObjStreamMissingFirst
	}

	if sd.N() == nil {
		return nil, errObjStreamMissingN
	}

	osd := types.ObjectStreamDict{
		StreamDict:     *sd,
		ObjCount:       *sd.N(),
		FirstObjOffset: *sd.First(),
		ObjArray:       nil}

	return &osd, nil
}
