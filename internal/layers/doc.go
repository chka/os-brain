// Package layers holds no code, only a test: the import rules between os-brain's internal
// packages. Each package may import only the packages below it in the table in
// layers_test.go, so the store never learns about a feature and one feature reaches another
// only where the table says it may. A new package fails the test until it is given a row.
package layers
