package demo

import (
	"errors"
	"strings"
)

func DoSomething(input string) (string, error) {
	if input == "" {
		return "", errors.New("empty")
	}
	result := input + "!"
	return result, nil
}

func helper(x int) int {
	if x < 0 {
		x = 0
	}
	return x * 2
}

func ParseHost(rawURL string) string {
	idx := strings.Index(rawURL, "://")
	if idx == -1 {
		return rawURL
	}
	rest := rawURL[idx+3:]
	slash := strings.Index(rest, "/")
	if slash == -1 {
		return rest
	}
	return rest[:slash]
}

func DivideUnsafe(a, b int) int {
	return a / b
}
