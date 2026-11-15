package sheet

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// ParseTime reads a time in seconds: a plain number such as 1425.5, or
// [HH:]MM:SS[.frac] such as 23:45.5 or 01:01:30.5. It is finite and not
// negative. With an hour field the minutes and seconds must be below 60;
// without one only the seconds must be, so 90:00 is ninety minutes.
func ParseTime(s string) (float64, error) {
	parts := strings.Split(s, ":")
	if len(parts) > 3 {
		return 0, fmt.Errorf("time %q has more than three fields", s)
	}
	last := parts[len(parts)-1]
	if !isDecimal(last) {
		return 0, fmt.Errorf("time %q is not seconds or [HH:]MM:SS", s)
	}
	total, err := strconv.ParseFloat(last, 64)
	if err != nil || math.IsInf(total, 0) {
		return 0, fmt.Errorf("time %q is out of range", s)
	}
	if len(parts) == 1 {
		return total, nil
	}
	if total >= 60 {
		return 0, fmt.Errorf("time %q has 60 or more seconds", s)
	}
	scale := 60.0
	for i := len(parts) - 2; i >= 0; i-- {
		field := parts[i]
		if field == "" || strings.Trim(field, "0123456789") != "" {
			return 0, fmt.Errorf("time %q is not seconds or [HH:]MM:SS", s)
		}
		v, err := strconv.ParseFloat(field, 64)
		if err != nil || math.IsInf(v, 0) {
			return 0, fmt.Errorf("time %q is out of range", s)
		}
		if len(parts) == 3 && i == 1 && v >= 60 {
			return 0, fmt.Errorf("time %q has 60 or more minutes beside an hour field", s)
		}
		total += v * scale
		scale *= 60
	}
	if math.IsInf(total, 0) {
		return 0, fmt.Errorf("time %q is out of range", s)
	}
	return total, nil
}

// isDecimal reports whether s is digits with an optional fraction: 5, 5.25,
// 5. or .25. Signs, exponents, hex and the words NaN and Inf are not.
func isDecimal(s string) bool {
	whole, frac, hasPoint := strings.Cut(s, ".")
	if strings.Trim(whole, "0123456789") != "" || strings.Trim(frac, "0123456789") != "" {
		return false
	}
	return whole != "" || (hasPoint && frac != "")
}
