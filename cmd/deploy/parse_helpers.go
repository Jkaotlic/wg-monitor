package main

import (
	"fmt"
	"strconv"
	"strings"
)

func parsePositivePort(s string) (int, error) {
	port, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || port <= 0 {
		return 0, fmt.Errorf("port must be a positive integer")
	}
	return port, nil
}
