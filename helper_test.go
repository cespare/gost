package main

import (
	. "launchpad.net/gocheck"
)

// approx is a gocheck checker for approximate equality of floats.
// Note that approximate equality of floats is a fraught topic. This is a very naive comparison.
type approxChecker struct {
	*CheckerInfo
}

var approx = &approxChecker{
	&CheckerInfo{Name: "approx", Params: []string{"obtained", "expected"}},
}

func (c *approxChecker) Check(params []interface{}, names []string) (result bool, error string) {
	f1, ok1 := params[0].(float64)
	f2, ok2 := params[1].(float64)
	if !(ok1 && ok2) {
		return false, "must compare float64s"
	}
	if f1 == f2 {
		return true, ""
	} else if f1 > f2 {
		f1, f2 = f2, f1
	}
	delta := (f2 - f1) / f1
	if delta < 0.0001 { // Accept f2 up to 0.01% greater than f1
		return true, ""
	}
	return false, ""
}
