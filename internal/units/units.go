// Package units formats CPU and memory amounts the way Kubernetes users read them.
package units

import (
	"strconv"
	"strings"
)

// CPU formats millicores: values below one core use the "m" suffix,
// whole and fractional cores are printed as plain numbers ("1", "1.5").
func CPU(milli int64) string {
	if milli == 0 {
		return "0"
	}
	if milli < 1000 && milli > -1000 {
		return strconv.FormatInt(milli, 10) + "m"
	}
	return trimFloat(float64(milli)/1000, 2)
}

// Cores formats millicores as a core count with a fixed precision, used in meters.
func Cores(milli int64) string {
	return trimFloat(float64(milli)/1000, 2)
}

var (
	binaryUnits  = []string{"", "Ki", "Mi", "Gi", "Ti", "Pi", "Ei"}
	decimalUnits = []string{"", "k", "M", "G", "T", "P", "E"}
)

// Bytes formats a byte count using binary suffixes ("512Mi", "1.5Gi").
func Bytes(b int64) string { return bytesIn(b, 1024, binaryUnits) }

// DecimalBytes formats a byte count using decimal suffixes ("500M", "3G"),
// for values a chart wrote in decimal units.
func DecimalBytes(b int64) string { return bytesIn(b, 1000, decimalUnits) }

// Grouped formats an integer with thousands separators ("3,000,000,000").
func Grouped(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	if neg {
		s = "-" + s
	}
	return s
}

func bytesIn(b int64, base float64, suffixes []string) string {
	if b == 0 {
		return "0"
	}
	neg := b < 0
	if neg {
		b = -b
	}
	v := float64(b)
	i := 0
	for v >= base && i < len(suffixes)-1 {
		v /= base
		i++
	}
	prec := 1
	if v >= 100 || i == 0 {
		prec = 0
	}
	s := trimFloat(v, prec) + suffixes[i]
	if neg {
		s = "-" + s
	}
	return s
}

// Plain formats a dimensionless quantity such as a GPU count.
func Plain(milli int64) string {
	if milli%1000 == 0 {
		return strconv.FormatInt(milli/1000, 10)
	}
	return trimFloat(float64(milli)/1000, 3)
}

func trimFloat(f float64, prec int) string {
	s := strconv.FormatFloat(f, 'f', prec, 64)
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	return s
}
