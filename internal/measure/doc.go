// Package measure keeps numeric series beside the memories: a reading with a unit, the day it
// was taken and where it came from, and an optional band that is the only thing a reading is
// ever judged against. Measures live in their own table, outside the memory index, so a
// number never outranks a fact in recall; the brief, the map and doctor read their summaries
// from here.
package measure
