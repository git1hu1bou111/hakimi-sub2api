// Test-only frozen pre-optimization implementation. Identifiers are renamed;
// literals and parser/error behavior are unchanged for differential testing.
package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	// Keep this aligned with the gateway's default maximum request-body size. A
	// caller with a larger custom limit must not make this compatibility pass an
	// unbounded parser or patch accumulator.
	oracleOpenAIResponsesToolSchemaMaxBodySize = 256 << 20
	oracleOpenAIResponsesToolSchemaMaxDepth    = 128
	oracleOpenAIResponsesToolSchemaMaxEdits    = 1 << 20
	oracleOpenAIResponsesObjectUnionMaxSize    = 1 << 20
	oracleOpenAIResponsesObjectUnionMaxDepth   = 32

	oracleOpenAIResponsesToolSchemaFallbackType = `"object"`
)

var errOracleOpenAIResponsesToolSchemaLimit = errors.New("OpenAI Responses tool schema safety limit exceeded")

// shouldRepairOpenAIResponsesNullToolSchemaType reports whether the upstream
// path requires a concrete object type at a function tool's parameter root.
// This defect is shared by the OpenAI, Anthropic, Grok, and CN-compatible paths.
func oracleShouldRepairOpenAIResponsesNullToolSchemaType(platform string) bool {
	return platform == PlatformOpenAI || platform == PlatformAnthropic || platform == PlatformGrok || IsCNProvider(platform)
}

// shouldSanitizeOpenAIResponsesToolSchemaPatterns is intentionally narrower:
// regex lookaround rejection is an OpenAI-specific schema constraint.
func oracleShouldSanitizeOpenAIResponsesToolSchemaPatterns(platform string) bool {
	return platform == PlatformOpenAI
}

func oracleSanitizeOpenAIResponsesToolSchemasForPlatform(body []byte, platform string) ([]byte, bool, error) {
	normalized := body
	changed := false
	if oracleShouldRepairOpenAIResponsesNullToolSchemaType(platform) {
		next, repaired, err := oracleSanitizeOpenAIResponsesToolParameterTypes(normalized)
		if err != nil {
			return body, false, fmt.Errorf("sanitize OpenAI Responses tool parameters: %w", err)
		}
		if repaired {
			normalized = next
			changed = true
		}
	}
	if oracleShouldSanitizeOpenAIResponsesToolSchemaPatterns(platform) {
		next, sanitized, err := oracleSanitizeOpenAIResponsesToolSchemaPatterns(normalized)
		if err != nil {
			return body, false, fmt.Errorf("sanitize OpenAI Responses tool schema patterns: %w", err)
		}
		if sanitized {
			normalized = next
			changed = true
		}
	}
	return normalized, changed, nil
}

// sanitizeOpenAIResponsesToolSchemaPatterns removes schema constraints whose
// regular expressions OpenAI's Python re validator rejects, including
// lookaround, non-Python named groups, and unsupported backslash escapes. It
// also drops patternProperties keys with such expressions. When dropping a key
// would otherwise close a schema object (additionalProperties: false), the
// object is reopened so arguments that matched the removed pattern stay
// representable. It deliberately does not descend into instance-valued
// keywords such as default, examples, const, or enum.
func oracleSanitizeOpenAIResponsesToolSchemaPatterns(body []byte) ([]byte, bool, error) {
	if len(body) == 0 || !oracleOpenAIResponsesBodyMayContainUnsupportedRegex(body) {
		return body, false, nil
	}
	return oracleSanitizeOpenAIResponsesToolSchemas(body, oracleOpenAIResponsesToolSchemaOptions{removeUnsupportedRegexPatterns: true})
}

// sanitizeOpenAIResponsesToolParameterTypes replaces an explicitly null type
// on a tool's parameter root with object. It also repairs the known Codex
// missing root type when it is unambiguously implied by an object-only
// oneOf/anyOf. Other missing types and nested property schemas are left
// unchanged.
func oracleSanitizeOpenAIResponsesToolParameterTypes(body []byte) ([]byte, bool, error) {
	if len(body) == 0 {
		return body, false, nil
	}
	return oracleSanitizeOpenAIResponsesToolSchemas(body, oracleOpenAIResponsesToolSchemaOptions{
		replaceNullParameterTypes:       true,
		injectObjectUnionRootObjectType: true,
		dropNullRequired:                true,
	})
}

func oracleOpenAIResponsesBodyMayContainUnsupportedRegex(body []byte) bool {
	// An opening parenthesis may be literal or encoded as JSON's canonical
	// Unicode escape. A backslash inside a JSON string is escaped as \\, so the
	// sequence \\<letter> exposes the decoded regex escape without parsing.
	if bytes.Contains(body, []byte("(")) || bytes.Contains(body, []byte(`\u0028`)) ||
		bytes.Contains(body, []byte(`\u005c`)) || bytes.Contains(body, []byte(`\u005C`)) {
		return true
	}
	for i := 0; i+2 < len(body); i++ {
		if body[i] != '\\' || body[i+1] != '\\' {
			continue
		}
		escaped := body[i+2]
		if oracleIsASCIILetter(escaped) && !oraclePythonRegexEscapeAllowed(escaped) {
			return true
		}
	}
	return false
}

// hasUnsupportedOpenAIRegexPattern reports whether Python re rejects pattern.
// OpenAI validates tool-schema patterns with Python re, which supports neither
// lookaround nor the .NET/Java (?<name>...) and (?'name'...) group forms, and
// reserves unknown ASCII-letter escapes such as \p and \R.
func oracleHasUnsupportedOpenAIRegexPattern(pattern string) bool {
	inCharacterClass := false
	for i := 0; i < len(pattern); i++ {
		switch pattern[i] {
		case '\\':
			if i+1 >= len(pattern) {
				return true
			}
			escaped := pattern[i+1]
			if oracleIsASCIILetter(escaped) && !oraclePythonRegexEscapeAllowed(escaped) {
				return true
			}
			i++
		case '[':
			inCharacterClass = true
		case ']':
			inCharacterClass = false
		case '(':
			if inCharacterClass || i+1 >= len(pattern) || pattern[i+1] != '?' {
				continue
			}
			if i+2 >= len(pattern) {
				return true
			}
			switch pattern[i+2] {
			case '=', '!', '<', '\'':
				return true
			}
		}
	}
	return false
}

func oracleIsASCIILetter(ch byte) bool {
	return ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z'
}

func oraclePythonRegexEscapeAllowed(ch byte) bool {
	switch ch {
	case 'a', 'b', 'f', 'n', 'r', 't', 'v',
		'd', 'D', 's', 'S', 'w', 'W',
		'A', 'Z', 'B', 'N', 'u', 'U', 'x':
		return true
	}
	return false
}

type oracleOpenAIResponsesToolSchemaOptions struct {
	removeUnsupportedRegexPatterns  bool
	replaceNullParameterTypes       bool
	injectObjectUnionRootObjectType bool
	dropNullRequired                bool
}

type oracleOpenAIResponsesToolSchemaContext uint8

const (
	oracleOpenAIResponsesToolSchemaSkip oracleOpenAIResponsesToolSchemaContext = iota
	oracleOpenAIResponsesToolSchemaDocument
	oracleOpenAIResponsesToolSchemaTools
	oracleOpenAIResponsesToolSchemaTool
	oracleOpenAIResponsesToolSchemaInput
	oracleOpenAIResponsesToolSchemaInputItem
	oracleOpenAIResponsesToolSchemaToolCarrier
	oracleOpenAIResponsesToolSchemaTypeProbe
	oracleOpenAIResponsesToolSchemaFunction
	oracleOpenAIResponsesToolSchema
	oracleOpenAIResponsesToolSchemaArray
	oracleOpenAIResponsesToolSchemaMap
	oracleOpenAIResponsesToolSchemaPatternPropertiesMap
	oracleOpenAIResponsesToolSchemaOrArray
)

type oracleOpenAIResponsesToolSchemaEdit struct {
	start       int
	end         int
	replacement string
}

type oracleOpenAIResponsesToolSchemaParser struct {
	body      []byte
	pos       int
	options   oracleOpenAIResponsesToolSchemaOptions
	edits     []oracleOpenAIResponsesToolSchemaEdit
	probeType string
	// patternPropertiesDrops records, per patternProperties object start byte,
	// how many unsupported pattern keys were removed. Parents consult it to
	// decide whether a closed object must be reopened.
	patternPropertiesDrops map[int]int
}

func oracleSanitizeOpenAIResponsesToolSchemas(
	body []byte, options oracleOpenAIResponsesToolSchemaOptions,
) ([]byte, bool, error) {
	if len(body) > oracleOpenAIResponsesToolSchemaMaxBodySize {
		return body, false, nil
	}
	if !utf8.Valid(body) {
		return nil, false, fmt.Errorf("sanitize OpenAI Responses tool schemas: invalid UTF-8")
	}
	p := oracleOpenAIResponsesToolSchemaParser{body: body, options: options}
	if err := p.parseValue(oracleOpenAIResponsesToolSchemaDocument, false, 0); err != nil {
		if errors.Is(err, errOracleOpenAIResponsesToolSchemaLimit) {
			return body, false, nil
		}
		return nil, false, err
	}
	p.skipWhitespace()
	if p.pos != len(body) {
		return nil, false, fmt.Errorf("sanitize OpenAI Responses tool schemas: trailing data at byte %d", p.pos)
	}
	if !json.Valid(body) {
		return nil, false, fmt.Errorf("sanitize OpenAI Responses tool schemas: invalid JSON")
	}
	if len(p.edits) == 0 {
		return body, false, nil
	}
	return oracleApplyOpenAIResponsesToolSchemaEdits(body, p.edits)
}

func (p *oracleOpenAIResponsesToolSchemaParser) parseValue(
	context oracleOpenAIResponsesToolSchemaContext, schemaRoot bool, depth int,
) error {
	if depth > oracleOpenAIResponsesToolSchemaMaxDepth {
		return errOracleOpenAIResponsesToolSchemaLimit
	}
	p.skipWhitespace()
	if p.pos >= len(p.body) {
		return p.syntaxError("expected value")
	}
	if p.body[p.pos] == '{' {
		switch context {
		case oracleOpenAIResponsesToolSchemaInput:
			context = oracleOpenAIResponsesToolSchemaInputItem
		case oracleOpenAIResponsesToolSchemaOrArray, oracleOpenAIResponsesToolSchemaArray:
			context = oracleOpenAIResponsesToolSchema
		}
	}
	if context == oracleOpenAIResponsesToolSchemaInputItem && p.body[p.pos] == '{' {
		probe := oracleOpenAIResponsesToolSchemaParser{body: p.body, pos: p.pos}
		if err := probe.parseValue(oracleOpenAIResponsesToolSchemaTypeProbe, false, depth); err != nil {
			return err
		}
		switch probe.probeType {
		case "function", "custom", "tool_search", "namespace":
			context = oracleOpenAIResponsesToolSchemaTool
		case "additional_tools":
			context = oracleOpenAIResponsesToolSchemaToolCarrier
		default:
			context = oracleOpenAIResponsesToolSchemaSkip
		}
	}
	switch p.body[p.pos] {
	case '{':
		return p.parseObject(context, schemaRoot, depth+1)
	case '[':
		return p.parseArray(context, depth+1)
	case '"':
		_, _, err := p.parseString()
		return err
	case 't':
		return p.parseKeyword("true")
	case 'f':
		return p.parseKeyword("false")
	case 'n':
		return p.parseKeyword("null")
	default:
		return p.parseNumber()
	}
}

func (p *oracleOpenAIResponsesToolSchemaParser) parseObject(
	context oracleOpenAIResponsesToolSchemaContext, schemaRoot bool, depth int,
) error {
	objectStart := p.pos
	p.pos++
	p.skipWhitespace()
	if p.consume('}') {
		return nil
	}
	previousComma := -1
	deleteRunStart := -1
	deleteRunPreviousComma := -1
	rootTypeCount := 0
	nullRootTypeStart := -1
	nullRootTypeEnd := -1
	parameterCount := 0
	parameterValueStart := -1
	parameterValueEnd := -1
	droppedPatternPropertyKeys := 0
	lostPatternProperty := false
	var additionalPropertiesEdits []oracleOpenAIResponsesToolSchemaEdit
	for {
		p.skipWhitespace()
		memberStart := p.pos
		keyStart, keyEnd, err := p.parseString()
		if err != nil {
			return err
		}
		key := p.body[keyStart:keyEnd]
		p.skipWhitespace()
		if !p.consume(':') {
			return p.syntaxError("expected colon")
		}
		p.skipWhitespace()
		valueStart := p.pos

		childContext, childSchemaRoot := oracleOpenAIResponsesToolSchemaChildContext(context, key)
		if err := p.parseValue(childContext, childSchemaRoot, depth); err != nil {
			return err
		}
		valueEnd := p.pos
		p.skipWhitespace()

		deleteMember := false
		if context == oracleOpenAIResponsesToolSchemaTypeProbe && oracleOpenAIResponsesJSONStringEquals(key, "type") {
			if value, ok := oracleDecodeOpenAIResponsesJSONStringValue(p.body[valueStart:valueEnd]); ok {
				p.probeType = strings.TrimSpace(value)
			}
		}
		if (context == oracleOpenAIResponsesToolSchemaTool || context == oracleOpenAIResponsesToolSchemaFunction) &&
			oracleOpenAIResponsesJSONStringEquals(key, "parameters") {
			parameterCount++
			parameterValueStart = valueStart
			parameterValueEnd = valueEnd
		}
		if p.options.removeUnsupportedRegexPatterns {
			if context == oracleOpenAIResponsesToolSchema && oracleOpenAIResponsesJSONStringEquals(key, "patternProperties") {
				// The child parser records how many keys it had to remove. A
				// closed sibling object must be reopened in that case.
				if p.patternPropertiesDrops[valueStart] > 0 {
					lostPatternProperty = true
				}
			}
			if context == oracleOpenAIResponsesToolSchema && oracleOpenAIResponsesJSONStringEquals(key, "additionalProperties") &&
				!bytes.Equal(p.body[valueStart:valueEnd], []byte("true")) {
				// Defer the edit: patternProperties may appear after this
				// member, and only actual key removal requires reopening.
				additionalPropertiesEdits = append(additionalPropertiesEdits, oracleOpenAIResponsesToolSchemaEdit{
					start:       valueStart,
					end:         valueEnd,
					replacement: "true",
				})
			}
			if context == oracleOpenAIResponsesToolSchema && oracleOpenAIResponsesJSONStringEquals(key, "pattern") {
				if pattern, ok := oracleDecodeOpenAIResponsesJSONStringValue(p.body[valueStart:valueEnd]); ok && oracleHasUnsupportedOpenAIRegexPattern(pattern) {
					deleteMember = true
				}
			}
			if context == oracleOpenAIResponsesToolSchemaPatternPropertiesMap {
				if pattern, ok := oracleDecodeOpenAIResponsesJSONStringValue(key); ok && oracleHasUnsupportedOpenAIRegexPattern(pattern) {
					deleteMember = true
				}
			}
		}
		// JSON Schema requires "required" to be an array. Clients occasionally emit
		// null, which xAI and Moonshot reject outright with a 400. An absent
		// "required" is the semantic equivalent of an empty one, so dropping the
		// member preserves the client's intent while satisfying strict validators.
		if context == oracleOpenAIResponsesToolSchema && p.options.dropNullRequired &&
			oracleOpenAIResponsesJSONStringEquals(key, "required") &&
			bytes.Equal(p.body[valueStart:valueEnd], []byte("null")) {
			deleteMember = true
		}
		if context == oracleOpenAIResponsesToolSchema && schemaRoot && oracleOpenAIResponsesJSONStringEquals(key, "type") {
			// Duplicate JSON keys have parser-dependent effective values. Repair a
			// root type only when it is unique rather than choosing a winner and
			// potentially changing the client's effective schema.
			rootTypeCount++
			if bytes.Equal(p.body[valueStart:valueEnd], []byte("null")) {
				nullRootTypeStart = valueStart
				nullRootTypeEnd = valueEnd
			}
		}

		if p.pos >= len(p.body) {
			return p.syntaxError("unterminated object")
		}
		if deleteMember && context == oracleOpenAIResponsesToolSchemaPatternPropertiesMap {
			droppedPatternPropertyKeys++
		}
		if deleteMember && deleteRunStart < 0 {
			deleteRunStart = memberStart
			deleteRunPreviousComma = previousComma
		}
		if !deleteMember && deleteRunStart >= 0 {
			// Remove through the delimiter after the deleted run, but retain the
			// whitespace that originally preceded the next member.
			if err := p.addEdit(deleteRunStart, previousComma+1, ""); err != nil {
				return err
			}
			deleteRunStart = -1
			deleteRunPreviousComma = -1
		}
		if p.body[p.pos] == ',' {
			comma := p.pos
			p.pos++
			previousComma = comma
			continue
		}
		if p.body[p.pos] != '}' {
			return p.syntaxError("expected comma or closing brace")
		}
		if deleteRunStart >= 0 {
			start := deleteRunStart
			if deleteRunPreviousComma >= 0 {
				start = deleteRunPreviousComma
			}
			if err := p.addEdit(start, p.pos, ""); err != nil {
				return err
			}
		}
		if p.options.replaceNullParameterTypes && rootTypeCount == 1 && nullRootTypeStart >= 0 {
			if err := p.addEdit(nullRootTypeStart, nullRootTypeEnd, oracleOpenAIResponsesToolSchemaFallbackType); err != nil {
				return err
			}
		}
		if p.options.injectObjectUnionRootObjectType &&
			(context == oracleOpenAIResponsesToolSchemaTool || context == oracleOpenAIResponsesToolSchemaFunction) &&
			parameterCount == 1 && parameterValueStart >= 0 {
			if edit, ok := oracleOpenAIMissingRootObjectUnionTypeEdit(
				p.body[parameterValueStart:parameterValueEnd], parameterValueStart,
			); ok {
				if err := p.addEdit(edit.start, edit.end, edit.replacement); err != nil {
					return err
				}
			}
		}
		if droppedPatternPropertyKeys > 0 {
			if p.patternPropertiesDrops == nil {
				p.patternPropertiesDrops = make(map[int]int)
			}
			p.patternPropertiesDrops[objectStart] = droppedPatternPropertyKeys
		}
		if lostPatternProperty {
			// Removing a patternProperties entry turns keys that only matched
			// that pattern into additional properties. Keeping
			// additionalProperties: false would reject argument keys the client
			// declared as valid, so reopen the object. This only widens what the
			// upstream accepts; it does not invent constraints.
			for _, edit := range additionalPropertiesEdits {
				if err := p.addEdit(edit.start, edit.end, edit.replacement); err != nil {
					return err
				}
			}
		}
		p.pos++
		return nil
	}
}

var (
	oracleOpenAIResponsesJSONOneOfKey     = []byte(`"oneOf"`)
	oracleOpenAIResponsesJSONAnyOfKey     = []byte(`"anyOf"`)
	oracleOpenAIResponsesJSONEscapeNeedle = []byte{'\\'}
)

func oracleOpenAIMissingRootObjectUnionTypeEdit(raw []byte, absoluteStart int) (oracleOpenAIResponsesToolSchemaEdit, bool) {
	if len(raw) > oracleOpenAIResponsesObjectUnionMaxSize {
		return oracleOpenAIResponsesToolSchemaEdit{}, false
	}
	if !bytes.Contains(raw, oracleOpenAIResponsesJSONOneOfKey) && !bytes.Contains(raw, oracleOpenAIResponsesJSONAnyOfKey) &&
		!bytes.Contains(raw, oracleOpenAIResponsesJSONEscapeNeedle) {
		return oracleOpenAIResponsesToolSchemaEdit{}, false
	}
	var schema map[string]json.RawMessage
	if len(raw) < 2 || raw[0] != '{' || json.Unmarshal(raw, &schema) != nil {
		return oracleOpenAIResponsesToolSchemaEdit{}, false
	}
	if _, hasType := schema["type"]; hasType || !oracleOpenAIResponsesSchemaHasObjectOnlyUnion(schema, 0) {
		return oracleOpenAIResponsesToolSchemaEdit{}, false
	}
	return oracleOpenAIResponsesToolSchemaEdit{
		start:       absoluteStart + 1,
		end:         absoluteStart + 1,
		replacement: `"type":"object",`,
	}, true
}

func oracleOpenAIResponsesSchemaHasObjectOnlyUnion(schema map[string]json.RawMessage, depth int) bool {
	if depth > oracleOpenAIResponsesObjectUnionMaxDepth {
		return false
	}
	found := false
	for _, keyword := range []string{"oneOf", "anyOf"} {
		raw, ok := schema[keyword]
		if !ok {
			continue
		}
		found = true
		var branches []json.RawMessage
		if json.Unmarshal(raw, &branches) != nil || len(branches) == 0 {
			return false
		}
		for _, branch := range branches {
			if !oracleOpenAIResponsesSchemaIsObjectOnly(branch, depth+1) {
				return false
			}
		}
	}
	return found
}

func oracleOpenAIResponsesSchemaIsObjectOnly(raw json.RawMessage, depth int) bool {
	if depth > oracleOpenAIResponsesObjectUnionMaxDepth {
		return false
	}
	var schema map[string]json.RawMessage
	if json.Unmarshal(raw, &schema) != nil {
		return false
	}
	if rawType, ok := schema["type"]; ok {
		var schemaType string
		return json.Unmarshal(rawType, &schemaType) == nil && schemaType == "object"
	}
	return oracleOpenAIResponsesSchemaHasObjectOnlyUnion(schema, depth+1)
}

func (p *oracleOpenAIResponsesToolSchemaParser) parseArray(
	context oracleOpenAIResponsesToolSchemaContext, depth int,
) error {
	p.pos++
	p.skipWhitespace()
	if p.consume(']') {
		return nil
	}
	childContext := oracleOpenAIResponsesToolSchemaSkip
	switch context {
	case oracleOpenAIResponsesToolSchemaTools:
		childContext = oracleOpenAIResponsesToolSchemaTool
	case oracleOpenAIResponsesToolSchemaInput:
		childContext = oracleOpenAIResponsesToolSchemaInputItem
	case oracleOpenAIResponsesToolSchemaArray, oracleOpenAIResponsesToolSchemaOrArray:
		childContext = oracleOpenAIResponsesToolSchema
	}
	for {
		if err := p.parseValue(childContext, false, depth); err != nil {
			return err
		}
		p.skipWhitespace()
		if p.consume(',') {
			continue
		}
		if !p.consume(']') {
			return p.syntaxError("expected comma or closing bracket")
		}
		return nil
	}
}

func oracleOpenAIResponsesToolSchemaChildContext(
	context oracleOpenAIResponsesToolSchemaContext, key []byte,
) (oracleOpenAIResponsesToolSchemaContext, bool) {
	switch context {
	case oracleOpenAIResponsesToolSchemaDocument:
		switch {
		case oracleOpenAIResponsesJSONStringEquals(key, "tools"):
			return oracleOpenAIResponsesToolSchemaTools, false
		case oracleOpenAIResponsesJSONStringEquals(key, "input"):
			return oracleOpenAIResponsesToolSchemaInput, false
		}
	case oracleOpenAIResponsesToolSchemaTool:
		switch {
		// Anthropic Messages carries the same JSON Schema under input_schema, so
		// /v1/messages tools must reach the sanitizer too.
		case oracleOpenAIResponsesJSONStringMatchesAny(key, "parameters", "input_schema"):
			return oracleOpenAIResponsesToolSchema, true
		case oracleOpenAIResponsesJSONStringEquals(key, "function"):
			return oracleOpenAIResponsesToolSchemaFunction, false
		case oracleOpenAIResponsesJSONStringEquals(key, "tools"):
			return oracleOpenAIResponsesToolSchemaTools, false
		}
	case oracleOpenAIResponsesToolSchemaToolCarrier:
		if oracleOpenAIResponsesJSONStringEquals(key, "tools") {
			return oracleOpenAIResponsesToolSchemaTools, false
		}
	case oracleOpenAIResponsesToolSchemaFunction:
		switch {
		case oracleOpenAIResponsesJSONStringEquals(key, "parameters"):
			return oracleOpenAIResponsesToolSchema, true
		case oracleOpenAIResponsesJSONStringEquals(key, "tools"):
			return oracleOpenAIResponsesToolSchemaTools, false
		}
	case oracleOpenAIResponsesToolSchema:
		switch {
		case oracleOpenAIResponsesJSONStringMatchesAny(key,
			"additionalProperties", "additionalItems", "contains", "not", "if", "then", "else",
			"propertyNames", "unevaluatedProperties", "unevaluatedItems", "contentSchema"):
			return oracleOpenAIResponsesToolSchema, false
		case oracleOpenAIResponsesJSONStringEquals(key, "items"):
			return oracleOpenAIResponsesToolSchemaOrArray, false
		case oracleOpenAIResponsesJSONStringMatchesAny(key, "anyOf", "oneOf", "allOf", "prefixItems"):
			return oracleOpenAIResponsesToolSchemaArray, false
		case oracleOpenAIResponsesJSONStringEquals(key, "patternProperties"):
			return oracleOpenAIResponsesToolSchemaPatternPropertiesMap, false
		case oracleOpenAIResponsesJSONStringMatchesAny(key,
			"properties", "$defs", "definitions", "dependentSchemas", "dependencies"):
			return oracleOpenAIResponsesToolSchemaMap, false
		}
	case oracleOpenAIResponsesToolSchemaMap, oracleOpenAIResponsesToolSchemaPatternPropertiesMap:
		return oracleOpenAIResponsesToolSchema, false
	}
	return oracleOpenAIResponsesToolSchemaSkip, false
}

func (p *oracleOpenAIResponsesToolSchemaParser) parseString() (int, int, error) {
	if p.pos >= len(p.body) || p.body[p.pos] != '"' {
		return 0, 0, p.syntaxError("expected string")
	}
	start := p.pos
	p.pos++
	for p.pos < len(p.body) {
		switch p.body[p.pos] {
		case '"':
			p.pos++
			return start, p.pos, nil
		case '\\':
			p.pos++
			if p.pos >= len(p.body) {
				return 0, 0, p.syntaxError("unterminated string escape")
			}
			switch p.body[p.pos] {
			case 'u':
				if p.pos+4 >= len(p.body) {
					return 0, 0, p.syntaxError("short unicode escape")
				}
				for _, digit := range p.body[p.pos+1 : p.pos+5] {
					if !oracleIsOpenAIResponsesJSONHexDigit(digit) {
						return 0, 0, p.syntaxError("invalid unicode escape")
					}
				}
				p.pos += 5
				continue
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
				p.pos++
			default:
				return 0, 0, p.syntaxError("invalid string escape")
			}
		default:
			if p.body[p.pos] < 0x20 {
				return 0, 0, p.syntaxError("control character in string")
			}
			p.pos++
		}
	}
	return 0, 0, p.syntaxError("unterminated string")
}

func oracleIsOpenAIResponsesJSONHexDigit(value byte) bool {
	return value >= '0' && value <= '9' || value >= 'a' && value <= 'f' || value >= 'A' && value <= 'F'
}

func (p *oracleOpenAIResponsesToolSchemaParser) parseKeyword(keyword string) error {
	if len(p.body)-p.pos < len(keyword) || string(p.body[p.pos:p.pos+len(keyword)]) != keyword {
		return p.syntaxError("invalid literal")
	}
	p.pos += len(keyword)
	return nil
}

func (p *oracleOpenAIResponsesToolSchemaParser) parseNumber() error {
	start := p.pos
	if p.consume('-') && p.pos >= len(p.body) {
		return p.syntaxError("invalid number")
	}
	if p.consume('0') {
		if p.pos < len(p.body) && p.body[p.pos] >= '0' && p.body[p.pos] <= '9' {
			return p.syntaxError("invalid leading zero")
		}
	} else {
		if p.pos >= len(p.body) || p.body[p.pos] < '1' || p.body[p.pos] > '9' {
			return p.syntaxError("invalid number")
		}
		for p.pos < len(p.body) && p.body[p.pos] >= '0' && p.body[p.pos] <= '9' {
			p.pos++
		}
	}
	if p.consume('.') {
		fractionStart := p.pos
		for p.pos < len(p.body) && p.body[p.pos] >= '0' && p.body[p.pos] <= '9' {
			p.pos++
		}
		if p.pos == fractionStart {
			return p.syntaxError("invalid fraction")
		}
	}
	if p.pos < len(p.body) && (p.body[p.pos] == 'e' || p.body[p.pos] == 'E') {
		p.pos++
		if p.pos < len(p.body) && (p.body[p.pos] == '+' || p.body[p.pos] == '-') {
			p.pos++
		}
		exponentStart := p.pos
		for p.pos < len(p.body) && p.body[p.pos] >= '0' && p.body[p.pos] <= '9' {
			p.pos++
		}
		if p.pos == exponentStart {
			return p.syntaxError("invalid exponent")
		}
	}
	if p.pos == start {
		return p.syntaxError("invalid value")
	}
	return nil
}

func (p *oracleOpenAIResponsesToolSchemaParser) addEdit(start, end int, replacement string) error {
	if start < 0 || end < start || end > len(p.body) {
		return p.syntaxError("invalid patch span")
	}
	if len(p.edits) >= oracleOpenAIResponsesToolSchemaMaxEdits {
		return errOracleOpenAIResponsesToolSchemaLimit
	}
	p.edits = append(p.edits, oracleOpenAIResponsesToolSchemaEdit{start: start, end: end, replacement: replacement})
	return nil
}

func (p *oracleOpenAIResponsesToolSchemaParser) skipWhitespace() {
	for p.pos < len(p.body) {
		switch p.body[p.pos] {
		case ' ', '\t', '\n', '\r':
			p.pos++
		default:
			return
		}
	}
}

func (p *oracleOpenAIResponsesToolSchemaParser) consume(want byte) bool {
	if p.pos < len(p.body) && p.body[p.pos] == want {
		p.pos++
		return true
	}
	return false
}

func (p *oracleOpenAIResponsesToolSchemaParser) syntaxError(message string) error {
	return fmt.Errorf("sanitize OpenAI Responses tool schemas: %s at byte %d", message, p.pos)
}

func oracleDecodeOpenAIResponsesJSONString(raw []byte) (string, error) {
	decoded, err := strconv.Unquote(string(raw))
	if err != nil {
		return "", err
	}
	return decoded, nil
}

func oracleOpenAIResponsesJSONStringEquals(raw []byte, want string) bool {
	if len(raw) < 2 || raw[0] != '"' || raw[len(raw)-1] != '"' {
		return false
	}
	if !bytes.Contains(raw, []byte{'\\'}) {
		return bytes.Equal(raw[1:len(raw)-1], []byte(want))
	}
	decoded, err := oracleDecodeOpenAIResponsesJSONString(raw)
	return err == nil && decoded == want
}

func oracleOpenAIResponsesJSONStringMatchesAny(raw []byte, candidates ...string) bool {
	for _, candidate := range candidates {
		if oracleOpenAIResponsesJSONStringEquals(raw, candidate) {
			return true
		}
	}
	return false
}

func oracleDecodeOpenAIResponsesJSONStringValue(raw []byte) (string, bool) {
	if len(raw) < 2 || raw[0] != '"' || raw[len(raw)-1] != '"' {
		return "", false
	}
	decoded, err := oracleDecodeOpenAIResponsesJSONString(raw)
	return decoded, err == nil
}

func oracleApplyOpenAIResponsesToolSchemaEdits(
	body []byte, edits []oracleOpenAIResponsesToolSchemaEdit,
) ([]byte, bool, error) {
	// Order by start; at equal starts keep zero-width insertions ahead of the
	// span they precede, then put wider spans first so an enclosing edit is
	// seen before the edits nested inside it.
	sort.Slice(edits, func(i, j int) bool {
		if edits[i].start != edits[j].start {
			return edits[i].start < edits[j].start
		}
		iEmpty, jEmpty := edits[i].start == edits[i].end, edits[j].start == edits[j].end
		if iEmpty != jEmpty {
			return iEmpty
		}
		return edits[i].end > edits[j].end
	})

	merged := edits[:0]
	for _, edit := range edits {
		if len(merged) == 0 || edit.start >= merged[len(merged)-1].end {
			merged = append(merged, edit)
			continue
		}
		previous := &merged[len(merged)-1]
		if edit.end <= previous.end {
			// The enclosing edit deletes or replaces this whole span with
			// content that does not depend on it, e.g. a dropped
			// patternProperties entry or an additionalProperties schema
			// reopened to true. Edits nested inside it are moot.
			continue
		}
		if previous.replacement != "" || edit.replacement != "" {
			return nil, false, fmt.Errorf("sanitize OpenAI Responses tool schemas: overlapping patches")
		}
		if edit.end > previous.end {
			previous.end = edit.end
		}
	}

	delta := 0
	for _, edit := range merged {
		delta += len(edit.replacement) - (edit.end - edit.start)
	}
	sanitized := make([]byte, 0, len(body)+delta)
	cursor := 0
	for _, edit := range merged {
		sanitized = append(sanitized, body[cursor:edit.start]...)
		sanitized = append(sanitized, edit.replacement...)
		cursor = edit.end
	}
	sanitized = append(sanitized, body[cursor:]...)
	if !json.Valid(sanitized) {
		return nil, false, fmt.Errorf("sanitize OpenAI Responses tool schemas: patch produced invalid JSON")
	}
	return sanitized, true, nil
}
