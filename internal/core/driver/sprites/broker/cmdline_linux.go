package broker

import (
	"errors"
	"os"
	"strconv"
	"strings"
)

func processCmdline(pid int) ([]string, error) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil {
		return nil, err
	}
	s := strings.TrimRight(string(b), "\x00")
	if s == "" {
		return nil, errors.New("empty cmdline")
	}
	return strings.Split(s, "\x00"), nil
}
