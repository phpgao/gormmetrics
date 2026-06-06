package userdef

import (
	"fmt"
	"strconv"
)

// ToFloat is the userdef equivalent of the helpers in mysql/ and
// postgres/ — coerces a driver-returned interface{} into float64.
func ToFloat(v interface{}) (float64, bool) {
	switch x := v.(type) {
	case nil:
		return 0, false
	case int64:
		return float64(x), true
	case float64:
		return x, true
	case int:
		return float64(x), true
	case int32:
		return float64(x), true
	case uint64:
		return float64(x), true
	case uint32:
		return float64(x), true
	case []byte:
		f, err := strconv.ParseFloat(string(x), 64)
		if err != nil {
			return 0, false
		}
		return f, true
	case string:
		f, err := strconv.ParseFloat(x, 64)
		if err != nil {
			return 0, false
		}
		return f, true
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}

// toString coerces a driver-returned interface{} to a string suitable
// for use as a label value.
func toString(v interface{}) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case []byte:
		return string(x)
	default:
		return fmt.Sprint(x)
	}
}
