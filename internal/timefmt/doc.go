// Package timefmt is the brain's clock and its spelling of time: epoch seconds as the
// stored form, the dates and day-parts every command prints, the relative gaps ("3 days
// ago"), the dates a person may type, and the provenance line under a memory. Stored
// timestamps are compared across editions and machines, so they are parsed and printed in
// one place.
package timefmt
