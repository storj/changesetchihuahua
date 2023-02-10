package app

import (
	"fmt"
	"regexp"
	"strings"
)

func applyStringTransformer(s, transformer string) (string, error) {
	if transformer == "" {
		return "", fmt.Errorf("invalid empty transformer")
	}
	switch transformer[0] {
	case 's':
		return applyStringTransformerSubst(s, transformer)
	default:
		return "", fmt.Errorf("invalid transformer syntax %q", transformer)
	}
}

func applyStringTransformerSubst(s, transformer string) (string, error) {
	splitter := transformer[1:2]
	parts := strings.Split(transformer[2:], splitter)
	if len(parts) != 3 {
		return "", fmt.Errorf("invalid transformer syntax %q (bad subst)", transformer)
	}
	n := 1
	switch parts[2] {
	case "":
	case "g":
		n = -1 // replace all matches, not just one
	default:
		return "", fmt.Errorf("invalid transformer syntax %q (bad flags)", transformer)
	}
	transformerRegexp, err := regexp.Compile(parts[0])
	if err != nil {
		return "", fmt.Errorf("invalid transformer syntax %q (bad regex: %w)", transformer, err)
	}
	replaceString := parts[1]
	result := make([]byte, 0, len(s))
	restOfString := 0
	for _, subMatches := range transformerRegexp.FindAllStringSubmatchIndex(s, n) {
		result = append(result, []byte(s[restOfString:subMatches[0]])...)
		result = transformerRegexp.ExpandString(result, replaceString, s, subMatches)
		restOfString = subMatches[1]
	}
	result = append(result, []byte(s[restOfString:])...)
	return string(result), nil
}
