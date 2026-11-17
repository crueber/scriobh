//go:build !race

package pdfread_test

// The race detector inflates allocation; budgets are asserted at full strength otherwise.
const memFactor = 1
